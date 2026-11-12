package fleet

import (
	"reflect"
	"testing"
)

func TestParseDigestReadsItemsAndTheirFields(t *testing.T) {
	doc := ParseDigest(`# Thu 12 Nov 09:22

## Needs your attention

- **relay-20** (relay): The live relay can send your facts to any server
  - Do: Deploy the fix before 10:00 with ` + "`cd ~/code/projects/relay && jj new lwnyosru`" + `
  - Why: ` + "`github-file`" + ` lets the model add its own flag,
    and the session was blocked from deploying.
  - Since: Wed 20:05.
  - It works with your current config.
- **cleanroom-6c9a**, **cleanroom-4657** (cleanroom): Two questions were interrupted
  - **Do:** Ask them again

## Finished
- **slk-12** (slk): Released 0.29.0 to the tap

## In progress
- **kira-ad** (~/code/configs): Porting the Emacs config
`)
	want := DigestDoc{Stamp: "Thu 12 Nov 09:22", Sections: []DigestSection{
		{Title: "Needs your attention", Items: []DigestItem{
			{
				Names: []string{"relay-20"}, Project: "relay",
				Issue: "The live relay can send your facts to any server",
				Do:    "Deploy the fix before 10:00 with `cd ~/code/projects/relay && jj new lwnyosru`",
				Why:   "`github-file` lets the model add its own flag, and the session was blocked from deploying.",
				Since: "Wed 20:05",
				Notes: []string{"It works with your current config."},
			},
			{
				Names: []string{"cleanroom-6c9a", "cleanroom-4657"}, Project: "cleanroom",
				Issue: "Two questions were interrupted", Do: "Ask them again",
			},
		}},
		{Title: "Finished", Items: []DigestItem{
			{Names: []string{"slk-12"}, Project: "slk", Issue: "Released 0.29.0 to the tap"},
		}},
		{Title: "In progress", Items: []DigestItem{
			{Names: []string{"kira-ad"}, Project: "~/code/configs", Issue: "Porting the Emacs config"},
		}},
	}}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got  %+v\nwant %+v", doc, want)
	}
}

// A digest from before items had a line for what to do is a paragraph to
// an item. Its first sentence is the issue and the rest a note, and a
// full stop inside a code span or a version number ends no sentence.
func TestParseDigestSplitsAParagraphAtItsFirstSentence(t *testing.T) {
	for _, c := range []struct {
		name, line string
		want       DigestItem
	}{
		{"two sentences", "- **relay-3a** (relay): Subscribe to topic `relay` on your ntfy server. Waiting since Wed 20:05.",
			DigestItem{Names: []string{"relay-3a"}, Project: "relay",
				Issue: "Subscribe to topic `relay` on your ntfy server.", Notes: []string{"Waiting since Wed 20:05."}}},
		{"one sentence", "- **slk-12** (slk): Upgrade with `brew upgrade --cask kirahowe/tap/slk`.",
			DigestItem{Names: []string{"slk-12"}, Project: "slk", Issue: "Upgrade with `brew upgrade --cask kirahowe/tap/slk`."}},
		{"a stop in a code span", "- **a** (p): Run `x. Y` first. Then wait.",
			DigestItem{Names: []string{"a"}, Project: "p", Issue: "Run `x. Y` first.", Notes: []string{"Then wait."}}},
		{"version numbers", "- **a** (p): Drafts for 0.27.0, 0.28.0 and 0.29.0 are waiting. Publish them.",
			DigestItem{Names: []string{"a"}, Project: "p", Issue: "Drafts for 0.27.0, 0.28.0 and 0.29.0 are waiting.", Notes: []string{"Publish them."}}},
		{"a question", "- **a**: Keep PLAN.md ignored? Old refs are yours to delete.",
			DigestItem{Names: []string{"a"}, Issue: "Keep PLAN.md ignored?", Notes: []string{"Old refs are yours to delete."}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			doc := ParseDigest("## Needs your attention\n" + c.line)
			if got := doc.Sections[0].Items; len(got) != 1 || !reflect.DeepEqual(got[0], c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

// What a model writes loosely still lands where it belongs: labelled
// lines that are not indented or are no bullets, text before the first
// heading, a bullet with no name, and a paragraph between items.
func TestParseDigestReadsLooseMarkdown(t *testing.T) {
	doc := ParseDigest(`Here is the digest.

## Needs your attention
* **a** (p) No colon after the project
- Do: Not indented
Why: no bullet at all
  and a second line of it
- Waiting since last Fri 22:57

Some text of its own
that runs on.
- no name on this one
`)
	want := DigestDoc{Sections: []DigestSection{
		{Items: []DigestItem{{Issue: "Here is the digest."}}},
		{Title: "Needs your attention", Items: []DigestItem{
			{Names: []string{"a"}, Project: "p", Issue: "No colon after the project",
				Do: "Not indented", Why: "no bullet at all and a second line of it", Since: "last Fri 22:57"},
			{Issue: "Some text of its own that runs on."},
			{Issue: "no name on this one"},
		}},
	}}
	if !reflect.DeepEqual(doc, want) {
		t.Fatalf("got  %+v\nwant %+v", doc, want)
	}
}

func TestDigestSectionKinds(t *testing.T) {
	for title, want := range map[string]DigestKind{
		"Needs your attention": DigestNeeds,
		"needs you":            DigestNeeds,
		"Finished":             DigestFinished,
		"Done":                 DigestFinished,
		"In progress":          DigestProgress,
		"Notes":                DigestOther,
		"":                     DigestOther,
	} {
		if got := (DigestSection{Title: title}).Kind(); got != want {
			t.Errorf("%q is kind %d, want %d", title, got, want)
		}
	}
}
