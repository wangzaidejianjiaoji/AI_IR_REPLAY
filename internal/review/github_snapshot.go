package review

import (
	"context"
	"fmt"
	"gvs-pr-agent/internal/config"
	"gvs-pr-agent/internal/github"
)

func (s *Service) checkPRVersion(ctx context.Context, repo config.RepoConfig, pr *github.PullRequest) error {
	fresh, err := s.gh.GetPR(ctx, repo.Owner, repo.Repo, pr.Number)
	if err != nil {
		return err
	}
	if fresh.Head.SHA != pr.Head.SHA || fresh.Base.SHA != pr.Base.SHA || fresh.Title != pr.Title || fresh.Body != pr.Body || fresh.State != "open" || fresh.HasConflict() {
		return fmt.Errorf("PR 版本或开启状态发生变化，放弃本次审核")
	}
	return nil
}
