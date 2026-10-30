package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhongyang/novel-studio/internal/domain"
)

// P0-03 fixture: a model "accept" verdict is only the formal_accepted phase.
// Canon publication is a separate directory promotion gated behind the
// actual_matched -> published transition, and the fixed phase table refuses to
// skip. The collaborative author-confirmation boundary (dual-pipeline plan
// section 6.2, implemented in P4-03) therefore only needs to stop advancement
// between actual_matched and published: without crossing that boundary no
// canonical byte can exist, regardless of what the model decided.
func TestChapterRenderModelAcceptAloneDoesNotPublishCanon(t *testing.T) {
	outputDir, transactions, identity, body := chapterRenderStoreFixture(t)

	if _, err := transactions.BeginBody(identity, body, bodyReadyEvidence(identity)); err != nil {
		t.Fatal(err)
	}
	if _, err := transactions.Advance(identity, domain.ChapterRenderPhaseCommitted, committedEvidence(identity)); err != nil {
		t.Fatal(err)
	}
	acceptEvidence := domain.ChapterRenderPhaseEvidence{
		ReviewVerdict:     "accept",
		ReviewDisposition: "p0-fixture: model accepted the rendered body",
		ReviewArtifacts: []domain.ChapterRenderArtifactBinding{
			{Path: "reviews/01.md", Digest: chapterRenderStoreDigest("review-p0")},
		},
	}
	if _, err := transactions.Advance(identity, domain.ChapterRenderPhaseFormalAccepted, acceptEvidence); err != nil {
		t.Fatal(err)
	}

	latest, err := transactions.LoadLatest(identity)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Phase != domain.ChapterRenderPhaseFormalAccepted {
		t.Fatalf("model accept advanced the transaction to %s, want formal_accepted", latest.Phase)
	}

	// Even with the model verdict on record, the phase machine rejects the
	// direct jump into publication: the intervening actual_matched phase (and,
	// once implemented, the author confirmation boundary before it) is mandatory.
	if _, err := transactions.Advance(identity, domain.ChapterRenderPhasePublished, domain.ChapterRenderPhaseEvidence{
		DirectoryPublishID:     identity.TransactionID,
		DirectoryPublishDigest: chapterRenderStoreDigest("directory-publish"),
	}); err == nil || !strings.Contains(err.Error(), "cannot advance") {
		t.Fatalf("formal_accepted advanced to published without the chain: %v", err)
	}

	receipts, err := transactions.LoadReceipts(identity)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.Phase == domain.ChapterRenderPhasePublished {
			t.Fatal("published receipt exists although the author boundary was never crossed")
		}
	}

	// The transaction root is a hidden sibling of the output directory; the
	// canonical output tree itself must not contain a single byte after a
	// model-accept-only run.
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, filepath.Base(entry.Name()))
		}
		t.Fatalf("canonical output directory is not empty after model-accept-only run: %v", names)
	}
}
