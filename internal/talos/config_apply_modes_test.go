package talos

import (
	"context"
	"errors"
	"github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"reflect"
	"strings"
	"testing"
)

type modeMachine struct {
	machine.MachineServiceClient
	calls      []string
	mode       machine.ApplyConfigurationRequest_Mode
	nodes      []string
	failStage  bool
	failReboot bool
}

func (m *modeMachine) ApplyConfiguration(ctx context.Context, r *machine.ApplyConfigurationRequest, _ ...grpc.CallOption) (*machine.ApplyConfigurationResponse, error) {
	m.calls = append(m.calls, "apply")
	m.mode = r.Mode
	md, _ := metadata.FromOutgoingContext(ctx)
	m.nodes = append(m.nodes, md.Get("node")...)
	if m.failStage {
		return nil, errors.New("secret=private-canary")
	}
	return &machine.ApplyConfigurationResponse{Messages: []*machine.ApplyConfiguration{{}}}, nil
}
func (m *modeMachine) Reboot(ctx context.Context, _ *machine.RebootRequest, _ ...grpc.CallOption) (*machine.RebootResponse, error) {
	m.calls = append(m.calls, "reboot")
	md, _ := metadata.FromOutgoingContext(ctx)
	m.nodes = append(m.nodes, md.Get("node")...)
	if m.failReboot {
		return nil, errors.New("secret=private-canary")
	}
	return &machine.RebootResponse{Messages: []*machine.Reboot{{}}}, nil
}
func TestConfigModeRebootStagesThenRebootsOnlySelectedNode(t *testing.T) {
	for _, tc := range []struct {
		mode    string
		dry     bool
		want    []string
		apiMode machine.ApplyConfigurationRequest_Mode
	}{{"staged", false, []string{"apply"}, machine.ApplyConfigurationRequest_STAGED}, {"reboot", true, []string{"apply"}, machine.ApplyConfigurationRequest_STAGED}, {"reboot", false, []string{"apply", "reboot"}, machine.ApplyConfigurationRequest_STAGED}, {"auto", false, []string{"apply"}, machine.ApplyConfigurationRequest_AUTO}} {
		t.Run(tc.mode+map[bool]string{true: "-dry", false: ""}[tc.dry], func(t *testing.T) {
			mock := &modeMachine{}
			mgr := &TalosManager{client: &client.Client{MachineClient: mock}}
			if err := mgr.ApplyNodeConfig(context.Background(), "10.0.0.2", []byte("test"), tc.mode, tc.dry); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(mock.calls, tc.want) || mock.mode != tc.apiMode {
				t.Fatalf("wrong mode sequence: %v %v", mock.calls, mock.mode)
			}
			for _, node := range mock.nodes {
				if node != "10.0.0.2" {
					t.Fatal("cross-node apply")
				}
			}
			if len(mock.nodes) != len(mock.calls) {
				t.Fatal("unscoped API request")
			}
		})
	}
}
func TestConfigRebootFailureDoesNotHidePartialOutcomeOrSecrets(t *testing.T) {
	for _, stageFailure := range []bool{true, false} {
		mock := &modeMachine{failStage: stageFailure, failReboot: !stageFailure}
		mgr := &TalosManager{client: &client.Client{MachineClient: mock}}
		err := mgr.ApplyNodeConfig(context.Background(), "10.0.0.2", []byte("test"), "reboot", false)
		if err == nil || strings.Contains(err.Error(), "private-canary") {
			t.Fatal("failed apply accepted or secret leaked")
		}
		if stageFailure && len(mock.calls) != 1 {
			t.Fatal("reboot after failed stage")
		}
		if !stageFailure && !strings.Contains(err.Error(), "uncertain") {
			t.Fatal("accepted stage / failed reboot outcome not explicit")
		}
	}
}
