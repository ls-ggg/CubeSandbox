// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package constants

import "testing"

func TestNormalizeSnapshotBackend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: SnapshotBackendXFS},
		{in: "xfs", want: SnapshotBackendXFS},
		{in: "XFS", want: SnapshotBackendXFS},
		{in: "cubecow", want: SnapshotBackendXFS},
		{in: "s3", want: SnapshotBackendS3},
		{in: "S3", want: SnapshotBackendS3},
		{in: "nfs", wantErr: true},
	}
	for _, tc := range cases {
		got, err := NormalizeSnapshotBackend(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("NormalizeSnapshotBackend(%q) err=nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("NormalizeSnapshotBackend(%q) err=%v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("NormalizeSnapshotBackend(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSnapshotRemoteStatus(t *testing.T) {
	t.Parallel()
	if got := SnapshotRemoteStatus(SnapshotBackendS3); got != RemoteStatusPending {
		t.Fatalf("s3 remote_status=%q, want %q", got, RemoteStatusPending)
	}
	if got := SnapshotRemoteStatus(SnapshotBackendXFS); got != "" {
		t.Fatalf("xfs remote_status=%q, want empty", got)
	}
}
