package coordinator

import (
	"errors"
	"gvs-pr-agent/internal/agent"
	"io"
	"log/slog"
	"testing"
)

func TestRoleNamesExcludeAllFailedRoles(t *testing.T) {
	c := &Coordinator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s := c.aggregate([]agent.Result{{Role: "security", Name: "安全", Err: errors.New("offline")}, {Role: "architect", Name: "架构"}}, nil, nil)
	if len(s.AgentNames) != 1 || s.AgentNames[0] != "架构" {
		t.Fatalf("failed role counted as success: %v", s.AgentNames)
	}
}

func TestZeroJobsIsFailed(t *testing.T) {
	c := &Coordinator{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if s := c.aggregate(nil, nil, nil); !s.AllFailed {
		t.Fatal("zero executed jobs treated as success")
	}
}

func TestPartialRoleFailureHasSeparateDenominators(t *testing.T){
	c:=&Coordinator{logger:slog.New(slog.NewTextHandler(io.Discard,nil))}
	s:=c.aggregate([]agent.Result{
		{Role:"security",Name:"安全"},{Role:"security",Name:"安全",Err:errors.New("partial")},
		{Role:"architect",Name:"架构"},{Role:"test",Name:"测试",Err:errors.New("failed")},
	},nil,nil)
	if s.SuccessfulJobs!=2||s.FailedJobs!=2||s.FullySuccessfulRoles!=1||s.PartiallyFailedRoles!=1||s.FullyFailedRoles!=1{t.Fatalf("incorrect execution denominators: %+v",s)}
}
