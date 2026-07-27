package provisioner

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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

	capacity := int64(1)

	server := armfake.VirtualMachineScaleSetsServer{
		Get: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetsClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachineScaleSetsClientGetResponse], errResp azfake.ErrorResponder) {
			resp.SetResponse(http.StatusOK, armcompute.VirtualMachineScaleSetsClientGetResponse{
				VirtualMachineScaleSet: armcompute.VirtualMachineScaleSet{
					SKU: &armcompute.SKU{
						Capacity: to.Ptr(capacity),
					},
				},
			}, nil)
			return
		},
		BeginUpdate: func(ctx context.Context, resourceGroupName string, vmScaleSetName string, parameters armcompute.VirtualMachineScaleSetUpdate, options *armcompute.VirtualMachineScaleSetsClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetsClientUpdateResponse], errResp azfake.ErrorResponder) {
			if parameters.SKU == nil || parameters.SKU.Capacity == nil {
				t.Errorf("expected capacity info")
			} else if *parameters.SKU.Capacity != 2 {
				t.Errorf("expected capacity 2, got %d", *parameters.SKU.Capacity)
			}
			if parameters.SKU != nil && parameters.SKU.Capacity != nil {
				capacity = *parameters.SKU.Capacity
			}
			resp.SetTerminalResponse(http.StatusOK, armcompute.VirtualMachineScaleSetsClientUpdateResponse{
				VirtualMachineScaleSet: armcompute.VirtualMachineScaleSet{
					SKU: &armcompute.SKU{
						Capacity: to.Ptr(capacity),
					},
				},
			}, nil)
			return
		},
	}

	vmServer := armfake.VirtualMachineScaleSetVMsServer{
		NewListPager: func(resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetVMsClientListOptions) (resp azfake.PagerResponder[armcompute.VirtualMachineScaleSetVMsClientListResponse]) {
			resp.AddPage(http.StatusOK, armcompute.VirtualMachineScaleSetVMsClientListResponse{
				VirtualMachineScaleSetVMListResult: armcompute.VirtualMachineScaleSetVMListResult{
					Value: []*armcompute.VirtualMachineScaleSetVM{
						{
							InstanceID: to.Ptr("0"),
							Properties: &armcompute.VirtualMachineScaleSetVMProperties{
								TimeCreated: to.Ptr(time.Now()),
							},
						},
						{
							InstanceID: to.Ptr("1"),
							Properties: &armcompute.VirtualMachineScaleSetVMProperties{
								TimeCreated: to.Ptr(time.Now()),
							},
						},
					},
				},
			}, nil)
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
	var err error
	p.client, err = armcompute.NewVirtualMachineScaleSetsClient(subID, &azfake.TokenCredential{}, options)
	if err != nil {
		t.Fatal(err)
	}
	p.vmClient, err = armcompute.NewVirtualMachineScaleSetVMsClient(subID, &azfake.TokenCredential{}, options)
	if err != nil {
		t.Fatal(err)
	}

	ids, err := p.ScaleUp(context.Background(), "hot", 1, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(ids) != 2 {
		t.Errorf("expected 2 IDs, got %d", len(ids))
	}
	if ids[0] != InstanceID(fmt.Sprintf("%s/0", hot)) {
		t.Errorf("expected ID %s/0, got %s", hot, ids[0])
	}
}
