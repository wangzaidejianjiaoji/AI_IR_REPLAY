package packager

import (
	"gvs-pr-agent/internal/diff"
	"strings"
	"testing"
)

func TestPackBoundedCoversEveryFileWithoutTruncation(t *testing.T) {
	files := []*diff.FileDiff{{Path: "a.go", Patch: strings.Repeat("a", 60)}, {Path: "b.go", Patch: strings.Repeat("b", 60)}}
	pkgs, err := PackBounded(files, 5, 100)
	if err != nil || len(pkgs) != 2 {
		t.Fatalf("packages=%v error=%v", pkgs, err)
	}
	for i, p := range pkgs {
		if p.DiffText(0) != files[i].Patch {
			t.Fatal("file content lost")
		}
	}
	if _, err := PackBounded([]*diff.FileDiff{{Path: "huge.go", Patch: strings.Repeat("x", 101)}}, 5, 100); err == nil {
		t.Fatal("oversized file silently accepted")
	}
}

func TestInferSpecialistCoverage(t *testing.T) {
	for _, tc := range []struct {
		path  string
		roles []string
	}{
		{"src/models/yolov5_model.cpp", []string{"ai", "ai_tool", "performance"}},
		{"src/core/model_manager.cpp", []string{"ai", "performance"}},
		{"streamer/inference_engine.cpp", []string{"ai", "performance"}},
		{"src/service/infer_service_impl.cpp", []string{"performance", "ui"}},
		{"test_model/latency_benchmark_policy_test.cpp", []string{"test", "ai", "performance"}},
		{"tests/resource_test.cc", []string{"test"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			got := AgentsFor(Package{Files: []*diff.FileDiff{{Path: tc.path}}})
			for _, r := range tc.roles {
				if !got[r] {
					t.Errorf("missing role %s", r)
				}
			}
		})
	}
}

func TestNewSpecialistCoverage(t *testing.T) {
	for _, path := range []string{"gvs/gvflow/node.go", "gvs/gvscheduler/job.go", "gvs/stream/stream.go", "gvs/cam/cam.go", "gvs/dnn/model.go", "gvs/track/track.go", "gvs/msg/msg.go", "gvs/gvrtmp/output.go", "gvs/fastcap/frame.go", "gvs/grdetect/detect.go", "gvs/skysegmenter/mask.go", "gvs/heatmap_filter/filter.go", "gvs/trajectory/point.go", "gvs/smooth/filter.go", "gvs/onvifcam/input.go", "gvs/pidrpc/frame.go", "src/models/model.cpp", "src/core/manager.cpp", "src/service/service.cpp", "streamer/pipeline.cpp", "test_model/model_test.cpp"} {
		t.Run(path, func(t *testing.T) {
			got := AgentsFor(Package{Files: []*diff.FileDiff{{Path: path}}})
			for _, role := range []string{"concurrency", "resource", "dataflow"} {
				if !got[role] {
					t.Errorf("missing %s", role)
				}
			}
		})
	}
	got := AgentsFor(Package{Files: []*diff.FileDiff{{Path: "newmodule/worker.go"}}})
	if !got["concurrency"] || !got["resource"] || got["dataflow"] {
		t.Fatalf("unexpected generic module roles: %v", got)
	}
}
