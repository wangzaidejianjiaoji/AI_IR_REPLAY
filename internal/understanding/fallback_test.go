package understanding

import (
	"context"
	"errors"
	"gvs-pr-agent/internal/codeup"
	"gvs-pr-agent/internal/llm"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type unavailableRepo struct{ fakeRepo }

func (*unavailableRepo) ListBranches(context.Context, string) ([]*codeup.Branch, error) {
	return nil, errors.New("offline")
}

type failingModel struct{}

func (failingModel) Chat(context.Context, string, []llm.Message) (string, llm.Usage, error) {
	return "", llm.Usage{}, errors.New("model offline")
}

func TestPrepareReturnsVersionedOldSnapshotOnFailure(t *testing.T) {
	for _, failure := range []string{"repository", "model"} {
		t.Run(failure, func(t *testing.T) {
			cfg := understandingConfig(t)
			os.MkdirAll(filepath.Dir(cfg.DocumentPath), 0755)
			old := "<!-- SOURCE-SHA: old -->\n# Known lifecycle"
			os.WriteFile(cfg.DocumentPath, []byte(old), 0600)
			var repo Repository = &fakeRepo{head: "new", diff: "+changed"}
			var model ChatClient = &fakeModel{}
			if failure == "repository" {
				repo = &unavailableRepo{}
			} else {
				model = failingModel{}
			}
			snap, _, err := New(cfg, repo, model, "seed", nil).Prepare(context.Background(), "model")
			if err == nil {
				t.Fatal("failure was hidden")
			}
			if snap.Document != old || snap.SourceSHA != "old" || snap.Updated {
				t.Fatalf("old context lost: %+v", snap)
			}
		})
	}
}

func TestBootstrapRefusesUnverifiedSeed(t *testing.T) {
	cfg := understandingConfig(t)
	model := &fakeModel{}
	_, _, err := New(cfg, &fakeRepo{head: "new"}, model, "architecture assumptions", nil).Prepare(context.Background(), "model")
	if err == nil || !strings.Contains(err.Error(), "源码快照") {
		t.Fatalf("unverified seed was stamped as current: %v", err)
	}
	if model.calls != 0 {
		t.Fatal("model called without source evidence")
	}
	if _, err := os.Stat(cfg.DocumentPath); !os.IsNotExist(err) {
		t.Fatal("unverified document written")
	}
}

func TestPrepareRejectsDifferentRepositoryKnowledge(t *testing.T){
	cfg:=understandingConfig(t)
	os.MkdirAll(filepath.Dir(cfg.DocumentPath),0755)
	os.WriteFile(cfg.DocumentPath,[]byte("<!-- SOURCE-REPOSITORY: other/repository -->\n<!-- SOURCE-SHA: same -->\n# foreign"),0600)
	snap,_,err:=New(cfg,&fakeRepo{head:"same"},&fakeModel{},"seed",nil).Prepare(context.Background(),"model")
	if err==nil||snap.Document!=""{t.Fatalf("foreign repository snapshot reused: %+v %v",snap,err)}
}
