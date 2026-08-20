// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package cubebox

import "testing"

func TestFormatRootfsDeletable(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":      "-",
		"true":  "true",
		"TRUE":  "true",
		"false": "false",
		"no":    "false",
		"maybe": "maybe",
	}
	for in, want := range cases {
		if got := formatRootfsDeletable(in); got != want {
			t.Fatalf("formatRootfsDeletable(%q)=%q want %q", in, got, want)
		}
	}
}

func TestShouldQueryRootfsDeletableSkipsXFS(t *testing.T) {
	t.Parallel()
	if shouldQueryRootfsDeletable("xfs") || shouldQueryRootfsDeletable("") || shouldQueryRootfsDeletable("cubecow") {
		t.Fatal("xfs/empty must not query")
	}
	if !shouldQueryRootfsDeletable("s3") || !shouldQueryRootfsDeletable("S3") {
		t.Fatal("s3 must query")
	}
}

func TestBatchRootfsDeletableOneNodeDialFailureIsUnknown(t *testing.T) {
	t.Parallel()
	got := batchRootfsDeletableOneNode("127.0.0.1", 1, []rootfsDeletableQuery{
		{SnapshotID: "snap-1", Backend: "s3"},
		{SnapshotID: "snap-2", Backend: "s3"},
	})
	if got["snap-1"] != rootfsDeletableUnknown || got["snap-2"] != rootfsDeletableUnknown {
		t.Fatalf("got=%v want unknown", got)
	}
}
