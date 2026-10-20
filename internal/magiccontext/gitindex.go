// Ported from cortexkit/magic-context (MIT License, Copyright (c) 2025 Ufuk Altinok): features/magic-context/git-commits.

// Git commit indexing for ctx_search, ported from cortexkit/magic-context
// (MIT) features/git-commits.

package magiccontext

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GitCommit is one indexed commit.
type GitCommit struct {
	SHA, ShortSHA, Message, Author string
	CommittedAt                    int64 // ms
}

// readGitCommits runs `git log` for the last maxCount non-merge commits on
// HEAD, optionally limited to sinceDays.
func readGitCommits(workingDir string, sinceDays, maxCount int) ([]GitCommit, error) {
	args := []string{"log", "HEAD", "--no-merges", "--max-count=" + strconv.Itoa(maxCount), "--format=%H%x1f%s%x1f%ae%x1f%ct%x1f%b%x1e"}
	if sinceDays > 0 {
		since := time.Now().Add(-time.Duration(sinceDays) * 24 * time.Hour).UTC().Format(time.RFC3339)
		args = append(args, "--since="+since)
	}
	args = append(args, "--")
	out, err := runGit(workingDir, 30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	var commits []GitCommit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\r\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.SplitN(rec, "\x1f", 5)
		if len(f) < 4 || len(f[0]) < 7 {
			continue
		}
		ct, _ := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		msg := strings.TrimSpace(f[1])
		if len(f) == 5 {
			if body := strings.TrimSpace(f[4]); body != "" {
				msg += "\n\n" + body
			}
		}
		commits = append(commits, GitCommit{SHA: f[0], ShortSHA: f[0][:7], Message: msg, Author: f[2], CommittedAt: ct * 1000})
	}
	return commits, nil
}

// IndexGitCommits refreshes mc_git_commits for the project from workingDir's
// HEAD. It is a no-op unless set.GitIndex and the project key is git-based.
// New commits are embedded when an embedder is given (best effort). Returns
// the number of newly inserted commits.
func (s *Store) IndexGitCommits(ctx context.Context, set Settings, workingDir, projectKey string, embed Embedder) (int, error) {
	if !set.GitIndex || !strings.HasPrefix(projectKey, "git:") {
		return 0, nil
	}
	maxCommits := set.GitMaxCommits
	if maxCommits <= 0 {
		maxCommits = 2000
	}
	commits, err := readGitCommits(workingDir, set.GitSinceDays, maxCommits)
	if err != nil {
		return 0, fmt.Errorf("git log: %w", err)
	}
	s.EnsureSearchIndex(ctx)
	var fresh []GitCommit
	now := nowMS()
	err = s.Immediate(ctx, func(q querier) error {
		fresh = fresh[:0]
		for _, c := range commits {
			res, err := q.ExecContext(ctx, `INSERT INTO mc_git_commits (sha, project_path, short_sha, message, author, committed_at, indexed_at)
VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(sha) DO NOTHING`, c.SHA, projectKey, c.ShortSHA, c.Message, c.Author, c.CommittedAt, now)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				fresh = append(fresh, c)
			}
		}
		_, err := q.ExecContext(ctx, `DELETE FROM mc_git_commits WHERE project_path = ? AND sha NOT IN (
SELECT sha FROM mc_git_commits WHERE project_path = ? ORDER BY committed_at DESC, sha LIMIT ?)`, projectKey, projectKey, maxCommits)
		return err
	})
	if err != nil {
		return 0, err
	}
	if embed != nil && set.EmbeddingModel != "" && len(fresh) > 0 {
		_ = s.embedGitCommits(ctx, embed, set.EmbeddingModel, fresh)
	}
	return len(fresh), nil
}

func (s *Store) embedGitCommits(ctx context.Context, embed Embedder, modelID string, commits []GitCommit) error {
	const batch = 32
	for start := 0; start < len(commits); start += batch {
		part := commits[start:min(start+batch, len(commits))]
		texts := make([]string, len(part))
		for i, c := range part {
			texts[i] = c.Message
		}
		vecs, err := embed(ctx, texts)
		if err != nil {
			return err
		}
		if len(vecs) != len(part) {
			return fmt.Errorf("embedder returned %d vectors for %d inputs", len(vecs), len(part))
		}
		now := nowMS()
		if err := s.Immediate(ctx, func(q querier) error {
			for i, c := range part {
				if len(vecs[i]) == 0 {
					continue
				}
				if _, err := q.ExecContext(ctx, `INSERT INTO mc_git_commit_embeddings (sha, model_id, embedding, created_at)
SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM mc_git_commits WHERE sha = ?)
ON CONFLICT(sha, model_id) DO UPDATE SET embedding = excluded.embedding, created_at = excluded.created_at`,
					c.SHA, modelID, encodeEmbedding(vecs[i]), now, c.SHA); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
