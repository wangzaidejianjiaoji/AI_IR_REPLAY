package reflector

import (
	"gvs-pr-agent/internal/agent"
	"io"
	"log/slog"
	"testing"
)

func TestPreserveCPPAdviceAndRejectInvalidGoAdvice(t *testing.T) {
	r := New(false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	got, _ := r.Reflect([]agent.Issue{
		{File: "model.cpp", Problem: "release", Code: "std::unique_ptr<Model> model;"},
		{File: "model.go", Problem: "release", Code: "std::unique_ptr<Model> model;"},
	}, nil)
	if len(got) != 2 {
		t.Fatalf("issues=%v", got)
	}
	if got[0].Code == "" {
		t.Error("valid C++ advice lost through Go parser")
	}
	if got[1].Code != "" {
		t.Error("invalid Go advice was retained")
	}
}
