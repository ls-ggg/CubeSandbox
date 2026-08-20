// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	CubeLog "github.com/tencentcloud/CubeSandbox/cubelog"
)

// s3InitRetryInterval is the pause between failed S3 init attempts.
var s3InitRetryInterval = 5 * time.Second

// ErrS3NotReady is returned for backend=s3 requests while the S3 cubecow
// handle and metadata base are still initializing (or retrying).
var ErrS3NotReady = errors.New("s3 storage is not ready (initializing)")

// ensureS3MetadataReadyFn is swapped in tests to avoid real mkfs/mount.
var ensureS3MetadataReadyFn = EnsureS3MetadataReady

// s3CowOverride lets the init loop run EnsureS3MetadataReady before
// publishing s3CowManager (so storeForBackend still returns ErrS3NotReady).
var (
	s3CowOverrideMu sync.Mutex
	s3CowOverride   *S3Cow
)

// initS3CowSync brings the S3 handle and metadata base up before the rest
// of storage init proceeds, so nothing downstream — boot recovery above all
// — has to cope with a backend that exists only later.
//
// One attempt, not a retry loop: where s3lvol is running this succeeds
// outright, and where it is absent (xfs-only nodes) retrying would delay
// every startup for a handle nobody asks for. A failure hands off to
// [local.startS3CowInitLoop] and is never fatal.
func (l *local) initS3CowSync(ctx context.Context) {
	if l == nil || !l.useCowStorage() || l.s3CowManager != nil {
		// A manager already present means the handle came from somewhere
		// else — a previous init, or an injected one. tryS3CowInitOnce
		// would drop it and build its own.
		return
	}
	if err := l.tryS3CowInitOnce(ctx); err != nil {
		CubeLog.Errorf("s3 cubecow init fail (background retry every %s takes over): %v",
			s3InitRetryInterval, err)
		return
	}
	CubeLog.Infof("cubecow s3 handle and metadata base ready before storage recovery")
}

func (l *local) startS3CowInitLoop(parent context.Context) {
	if l == nil || !l.useCowStorage() {
		return
	}
	if l.s3CowManager != nil {
		// Synchronous init already published the handle (or a test injected
		// one); retrying would only tear it down and rebuild it.
		return
	}
	l.stopS3CowInitLoop()
	ctx, cancel := context.WithCancel(parent)
	l.s3InitCancel = cancel
	go l.runS3CowInitLoop(ctx)
}

func (l *local) stopS3CowInitLoop() {
	if l == nil || l.s3InitCancel == nil {
		return
	}
	l.s3InitCancel()
	l.s3InitCancel = nil
}

func (l *local) runS3CowInitLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if err := l.tryS3CowInitOnce(ctx); err != nil {
			CubeLog.Errorf("s3 cubecow init attempt fail (retry in %s): %v", s3InitRetryInterval, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(s3InitRetryInterval):
			}
			continue
		}
		CubeLog.Infof("cubecow s3 handle and metadata base ready")
		// Boot recovery may have run before this handle existed and skipped
		// every s3-backed entry. Redo that pass now; it is idempotent, and
		// at this point the node is already serving, so a failure here is
		// reported rather than fatal.
		if err := l.RecoverStorageState(ctx); err != nil {
			CubeLog.Warnf("storage recover after s3 became ready: %v", err)
		}
		return
	}
}

// tryS3CowInitOnce initializes the S3 cubecow engine and metadata base.
// On success it publishes s3CowEngine / s3CowManager. On failure it leaves
// them unset (and closes any partial engine).
func (l *local) tryS3CowInitOnce(ctx context.Context) error {
	if l == nil {
		return fmt.Errorf("storage is not initialized")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if l.s3CowManager != nil && l.s3CowEngine != nil {
		return nil
	}

	l.clearS3Cow()

	engine, source, err := initS3CowEngine(l.config)
	if err != nil {
		return fmt.Errorf("s3 cubecow handle init: %w", err)
	}
	mgr := newS3CowVolumeManager(engine)

	setS3CowOverride(mgr)
	metaErr := ensureS3MetadataReadyFn(ctx)
	setS3CowOverride(nil)
	if metaErr != nil {
		engine.Close()
		return fmt.Errorf("s3 metadata base init: %w", metaErr)
	}
	if ctx.Err() != nil {
		engine.Close()
		return ctx.Err()
	}

	l.s3CowEngine = engine
	l.s3CowManager = mgr
	CubeLog.Infof("cubecow s3 handle initialized from %s", source)
	return nil
}

func setS3CowOverride(m *S3Cow) {
	s3CowOverrideMu.Lock()
	s3CowOverride = m
	s3CowOverrideMu.Unlock()
}

func (l *local) clearS3Cow() {
	if l == nil {
		return
	}
	setS3CowOverride(nil)
	if l.s3CowEngine != nil {
		l.s3CowEngine.Close()
		l.s3CowEngine = nil
	}
	l.s3CowManager = nil
}
