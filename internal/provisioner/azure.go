package provisioner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
)

type AzureVMSSProvisioner struct {
	SubscriptionID string
	ResourceGroup  string
	HotVMSS        string
	BurstVMSS      string

	client   *armcompute.VirtualMachineScaleSetsClient
	vmClient *armcompute.VirtualMachineScaleSetVMsClient
}

func NewAzureVMSSProvisioner(subID, rg, hot, burst string) (*AzureVMSSProvisioner, error) {
	return NewAzureVMSSProvisionerWithOptions(subID, rg, hot, burst, nil)
}

func NewAzureVMSSProvisionerWithOptions(subID, rg, hot, burst string, options *arm.ClientOptions) (*AzureVMSSProvisioner, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return nil, err
	}

	client, err := armcompute.NewVirtualMachineScaleSetsClient(subID, cred, options)
	if err != nil {
		return nil, err
	}

	vmClient, err := armcompute.NewVirtualMachineScaleSetVMsClient(subID, cred, options)
	if err != nil {
		return nil, err
	}

	return &AzureVMSSProvisioner{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		HotVMSS:        hot,
		BurstVMSS:      burst,
		client:         client,
		vmClient:       vmClient,
	}, nil
}

func (p *AzureVMSSProvisioner) ScaleUp(ctx context.Context, pool string, n int, labels map[string]string) ([]InstanceID, error) {
	vmssName := p.HotVMSS
	if pool == "burst" {
		vmssName = p.BurstVMSS
	}

	if vmssName == "" {
		return nil, fmt.Errorf("VMSS name for pool %s is not configured", pool)
	}

	// 1. Get current capacity
	vmss, err := p.client.Get(ctx, p.ResourceGroup, vmssName, nil)
	if err != nil {
		return nil, fmt.Errorf("get vmss %s: %w", vmssName, err)
	}

	if vmss.SKU == nil || vmss.SKU.Capacity == nil {
		return nil, fmt.Errorf("vmss %s has no SKU/capacity info", vmssName)
	}

	currentCapacity := *vmss.SKU.Capacity
	newCapacity := currentCapacity + int64(n)

	// 2. Update capacity
	poller, err := p.client.BeginUpdate(ctx, p.ResourceGroup, vmssName, armcompute.VirtualMachineScaleSetUpdate{
		SKU: &armcompute.SKU{
			Capacity: &newCapacity,
		},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update capacity for %s: %w", vmssName, err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("wait for update capacity for %s: %w", vmssName, err)
	}

	// 3. List instances to return their IDs
	instances, err := p.listVMSSInstances(ctx, pool, vmssName)
	if err != nil {
		return nil, err
	}

	var ids []InstanceID
	for _, inst := range instances {
		ids = append(ids, inst.ID)
	}
	return ids, nil
}

func (p *AzureVMSSProvisioner) ScaleDown(ctx context.Context, ids []InstanceID) error {
	// Group IDs by VMSS
	byVMSS := make(map[string][]string)
	for _, id := range ids {
		parts := strings.Split(string(id), "/")
		if len(parts) != 2 {
			continue // Invalid ID format
		}
		vmssName := parts[0]
		instanceID := parts[1]
		byVMSS[vmssName] = append(byVMSS[vmssName], instanceID)
	}

	for vmssName, instanceIDs := range byVMSS {
		ptrs := make([]*string, len(instanceIDs))
		for i := range instanceIDs {
			ptrs[i] = &instanceIDs[i]
		}

		poller, err := p.client.BeginDeleteInstances(ctx, p.ResourceGroup, vmssName, armcompute.VirtualMachineScaleSetVMInstanceRequiredIDs{
			InstanceIDs: ptrs,
		}, nil)
		if err != nil {
			return fmt.Errorf("begin delete instances from %s: %w", vmssName, err)
		}
		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			return fmt.Errorf("wait for delete instances from %s: %w", vmssName, err)
		}
	}

	return nil
}

func (p *AzureVMSSProvisioner) ListInstances(ctx context.Context) ([]Instance, error) {
	hot, err := p.listVMSSInstances(ctx, "hot", p.HotVMSS)
	if err != nil {
		return nil, err
	}
	burst, err := p.listVMSSInstances(ctx, "burst", p.BurstVMSS)
	if err != nil {
		return nil, err
	}
	return append(hot, burst...), nil
}

func (p *AzureVMSSProvisioner) listVMSSInstances(ctx context.Context, pool, vmssName string) ([]Instance, error) {
	if vmssName == "" {
		return nil, nil
	}
	pager := p.vmClient.NewListPager(p.ResourceGroup, vmssName, nil)
	var instances []Instance
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list vmss vms for %s: %w", vmssName, err)
		}
		for _, vm := range page.Value {
			if vm.InstanceID == nil {
				continue
			}
			id := fmt.Sprintf("%s/%s", vmssName, *vm.InstanceID)
			createdAt := time.Now()
			if vm.Properties != nil && vm.Properties.TimeCreated != nil {
				createdAt = *vm.Properties.TimeCreated
			}

			instances = append(instances, Instance{
				ID:        InstanceID(id),
				Pool:      pool,
				CreatedAt: createdAt,
				Labels:    make(map[string]string),
			})
		}
	}
	return instances, nil
}
