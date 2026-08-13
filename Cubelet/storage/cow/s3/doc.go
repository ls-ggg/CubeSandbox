// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

// Package s3 holds the canonical backend name for the S3 CoW Store.
//
// The runtime mock type lives in package storage as [storage.S3Cow]: a copy of
// the XFS/reflink Store path that shares the cubecow engine until a real remote
// S3 backend exists. Do not put mock logic into [storage.XfsCow].
package s3

import "github.com/tencentcloud/CubeSandbox/Cubelet/storage/cow"

// Name is the Store.Name() value for the S3-backed backend.
const Name = cow.NameS3
