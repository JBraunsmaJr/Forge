package provisioner

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	armfake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6/fake"
)

type dispatchTransport struct {
	vmssTransport interface {
		Do(*http.Request) (*http.Response, error)
	}
	vmTransport interface {
		Do(*http.Request) (*http.Response, error)
	}
}

func (t *dispatchTransport) Do(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/virtualMachines") {
		return t.vmTransport.Do(req)
	}
	return t.vmssTransport.Do(req)
}

func TestAzureVMSSProvisioner_ScaleUp(t *testing.T) {
	const (
		subID = "sub-id"
		rg    = "rg"
		hot   = "hot-vmss"
		burst = "burst-vmss"
	)

	mu := sync.Mutex{}
	vms := []*armcompute.VirtualMachineScaleSetVM{
		{
			InstanceID: to.Ptr("0"),
			Properties: &armcompute.VirtualMachineScaleSetVMProperties{
				TimeCreated: to.Ptr(time.Now()),
			},
		},
	}

	server := armfake.VirtualMachineScaleSetsServer{
		Get: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetsClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachineScaleSetsClientGetResponse], errResp azfake.ErrorResponder) {
			mu.Lock()
			defer mu.Unlock()
			resp.SetResponse(http.StatusOK, armcompute.VirtualMachineScaleSetsClientGetResponse{
				VirtualMachineScaleSet: armcompute.VirtualMachineScaleSet{
					Location: to.Ptr("eastus"),
					SKU: &armcompute.SKU{
						Capacity: to.Ptr(int64(len(vms))),
					},
				},
			}, nil)
			return
		},
		BeginUpdate: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, parameters armcompute.VirtualMachineScaleSetUpdate, options *armcompute.VirtualMachineScaleSetsClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetsClientUpdateResponse], errResp azfake.ErrorResponder) {
			mu.Lock()
			defer mu.Unlock()
			if parameters.SKU != nil && parameters.SKU.Capacity != nil {
				newCap := *parameters.SKU.Capacity
				for int64(len(vms)) < newCap {
					vms = append(vms, &armcompute.VirtualMachineScaleSetVM{
						InstanceID: to.Ptr(fmt.Sprintf("%d", len(vms))),
						Properties: &armcompute.VirtualMachineScaleSetVMProperties{
							TimeCreated: to.Ptr(time.Now()),
						},
					})
				}
			}
			resp.SetTerminalResponse(http.StatusOK, armcompute.VirtualMachineScaleSetsClientUpdateResponse{
				VirtualMachineScaleSet: armcompute.VirtualMachineScaleSet{
					SKU: &armcompute.SKU{
						Capacity: to.Ptr(int64(len(vms))),
					},
				},
			}, nil)
			return
		},
	}

	vmServer := armfake.VirtualMachineScaleSetVMsServer{
		NewListPager: func(resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetVMsClientListOptions) (resp azfake.PagerResponder[armcompute.VirtualMachineScaleSetVMsClientListResponse]) {
			mu.Lock()
			defer mu.Unlock()
			resp.AddPage(http.StatusOK, armcompute.VirtualMachineScaleSetVMsClientListResponse{
				VirtualMachineScaleSetVMListResult: armcompute.VirtualMachineScaleSetVMListResult{
					Value: vms,
				},
			}, nil)
			return
		},
		BeginUpdate: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, instanceID string, parameters armcompute.VirtualMachineScaleSetVM, options *armcompute.VirtualMachineScaleSetVMsClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetVMsClientUpdateResponse], errResp azfake.ErrorResponder) {
			mu.Lock()
			defer mu.Unlock()
			if parameters.Location == nil || *parameters.Location != "eastus" {
				errResp.SetError(fmt.Errorf("missing or incorrect location"))
				return
			}
			for _, vm := range vms {
				if *vm.InstanceID == instanceID {
					vm.Tags = parameters.Tags
				}
			}
			resp.SetTerminalResponse(http.StatusOK, armcompute.VirtualMachineScaleSetVMsClientUpdateResponse{}, nil)
			return
		},
	}

	transport := &dispatchTransport{
		vmssTransport: armfake.NewVirtualMachineScaleSetsServerTransport(&server),
		vmTransport:   armfake.NewVirtualMachineScaleSetVMsServerTransport(&vmServer),
	}

	options := &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transport,
		},
	}

	p := &AzureVMSSProvisioner{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		HotVMSS:        hot,
		BurstVMSS:      burst,
	}
	p.client, _ = armcompute.NewVirtualMachineScaleSetsClient(subID, &azfake.TokenCredential{}, options)
	p.vmClient, _ = armcompute.NewVirtualMachineScaleSetVMsClient(subID, &azfake.TokenCredential{}, options)

	ids, err := p.ScaleUp(context.Background(), "hot", 2, map[string]string{"foo": "bar"})
	if err != nil {
		t.Fatal(err)
	}

	if len(ids) != 2 {
		t.Errorf("expected 2 new IDs, got %d", len(ids))
	}
	for i, id := range ids {
		expected := InstanceID(fmt.Sprintf("%s/%d", hot, i+1))
		if id != expected {
			t.Errorf("expected ID %s, got %s", expected, id)
		}
	}

	// Verify labels (tags)
	for _, vm := range vms {
		if *vm.InstanceID == "0" {
			if vm.Tags != nil {
				t.Errorf("VM 0 should not have tags")
			}
			continue
		}
		v, ok := vm.Tags["foo"]
		if !ok || v == nil || *v != "bar" {
			t.Errorf("VM %s should have tag foo=bar", *vm.InstanceID)
		}
	}
}

func TestAzureVMSSProvisioner_ScaleDown(t *testing.T) {
	const (
		subID = "sub-id"
		rg    = "rg"
		hot   = "hot-vmss"
	)

	deleted := make(map[string]bool)
	server := armfake.VirtualMachineScaleSetsServer{
		BeginDeleteInstances: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, parameters armcompute.VirtualMachineScaleSetVMInstanceRequiredIDs, options *armcompute.VirtualMachineScaleSetsClientBeginDeleteInstancesOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetsClientDeleteInstancesResponse], errResp azfake.ErrorResponder) {
			for _, id := range parameters.InstanceIDs {
				deleted[*id] = true
			}
			resp.SetTerminalResponse(http.StatusOK, armcompute.VirtualMachineScaleSetsClientDeleteInstancesResponse{}, nil)
			return
		},
	}

	transport := &dispatchTransport{
		vmssTransport: armfake.NewVirtualMachineScaleSetsServerTransport(&server),
	}

	options := &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transport,
		},
	}

	p := &AzureVMSSProvisioner{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		HotVMSS:        hot,
	}
	p.client, _ = armcompute.NewVirtualMachineScaleSetsClient(subID, &azfake.TokenCredential{}, options)

	// Test valid scale down
	err := p.ScaleDown(context.Background(), []InstanceID{InstanceID(hot + "/1"), InstanceID(hot + "/2")})
	if err != nil {
		t.Fatal(err)
	}
	if !deleted["1"] || !deleted["2"] {
		t.Errorf("expected instances 1 and 2 to be deleted")
	}

	// Test invalid ID format
	err = p.ScaleDown(context.Background(), []InstanceID{InstanceID("invalid-id")})
	if err == nil || !strings.Contains(err.Error(), "invalid instance ID format") {
		t.Errorf("expected error for invalid ID format, got %v", err)
	}
}

func TestAzureVMSSProvisioner_ListInstances(t *testing.T) {
	const (
		subID = "sub-id"
		rg    = "rg"
		hot   = "hot-vmss"
		burst = "burst-vmss"
	)

	vmServer := armfake.VirtualMachineScaleSetVMsServer{
		NewListPager: func(resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetVMsClientListOptions) (resp azfake.PagerResponder[armcompute.VirtualMachineScaleSetVMsClientListResponse]) {
			var vms []*armcompute.VirtualMachineScaleSetVM
			if vmScaleSetName == hot {
				vms = append(vms, &armcompute.VirtualMachineScaleSetVM{
					InstanceID: to.Ptr("h1"),
					Tags:       map[string]*string{"pool": to.Ptr("hot")},
				})
			} else {
				vms = append(vms, &armcompute.VirtualMachineScaleSetVM{
					InstanceID: to.Ptr("b1"),
					Tags:       map[string]*string{"pool": to.Ptr("burst")},
				})
			}
			resp.AddPage(http.StatusOK, armcompute.VirtualMachineScaleSetVMsClientListResponse{
				VirtualMachineScaleSetVMListResult: armcompute.VirtualMachineScaleSetVMListResult{
					Value: vms,
				},
			}, nil)
			return
		},
	}

	transport := &dispatchTransport{
		vmTransport: armfake.NewVirtualMachineScaleSetVMsServerTransport(&vmServer),
	}

	options := &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transport,
		},
	}

	p := &AzureVMSSProvisioner{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		HotVMSS:        hot,
		BurstVMSS:      burst,
	}
	p.vmClient, _ = armcompute.NewVirtualMachineScaleSetVMsClient(subID, &azfake.TokenCredential{}, options)

	instances, err := p.ListInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(instances) != 2 {
		t.Errorf("expected 2 instances, got %d", len(instances))
	}

	foundHot := false
	foundBurst := false
	for _, inst := range instances {
		if inst.ID == InstanceID(hot+"/h1") {
			foundHot = true
			if inst.Labels["pool"] != "hot" {
				t.Errorf("expected label pool=hot, got %s", inst.Labels["pool"])
			}
		}
		if inst.ID == InstanceID(burst+"/b1") {
			foundBurst = true
			if inst.Labels["pool"] != "burst" {
				t.Errorf("expected label pool=burst, got %s", inst.Labels["pool"])
			}
		}
	}
	if !foundHot || !foundBurst {
		t.Errorf("did not find both hot and burst instances")
	}
}

func TestAzureVMSSProvisioner_ScaleDown_AggregateErrors(t *testing.T) {
	const (
		subID = "sub-id"
		rg    = "rg"
		hot   = "hot-vmss"
		burst = "burst-vmss"
	)

	server := armfake.VirtualMachineScaleSetsServer{
		BeginDeleteInstances: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, parameters armcompute.VirtualMachineScaleSetVMInstanceRequiredIDs, options *armcompute.VirtualMachineScaleSetsClientBeginDeleteInstancesOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetsClientDeleteInstancesResponse], errResp azfake.ErrorResponder) {
			errResp.SetError(fmt.Errorf("failed to delete from %s", vmScaleSetName))
			return
		},
	}

	transport := &dispatchTransport{
		vmssTransport: armfake.NewVirtualMachineScaleSetsServerTransport(&server),
	}

	options := &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: transport,
		},
	}

	p := &AzureVMSSProvisioner{
		SubscriptionID: subID,
		ResourceGroup:  rg,
		HotVMSS:        hot,
		BurstVMSS:      burst,
	}
	p.client, _ = armcompute.NewVirtualMachineScaleSetsClient(subID, &azfake.TokenCredential{}, options)

	err := p.ScaleDown(context.Background(), []InstanceID{InstanceID(hot + "/1"), InstanceID(burst + "/2")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "failed to delete from "+hot) {
		t.Errorf("expected error from hot VMSS, got: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to delete from "+burst) {
		t.Errorf("expected error from burst VMSS, got: %v", err)
	}
}
