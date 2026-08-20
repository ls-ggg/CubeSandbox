// Copyright (c) 2024 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0
//

package appsnapshot

import (
	"context"
	"fmt"
	"strings"

	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/tencentcloud/CubeSandbox/Cubelet/api/services/errorcode/v1"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/constants"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/controller/runtemplate"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/log"
	"github.com/tencentcloud/CubeSandbox/Cubelet/pkg/ret"
	"github.com/tencentcloud/CubeSandbox/Cubelet/plugins/workflow"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: constants.InternalPlugin,
		ID:   constants.APPSnapshotID.ID(),
		Requires: []plugin.Type{
			constants.ControllerPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			obj, err := ic.GetByID(constants.ControllerPlugin, constants.PluginRunTemplateManager.ID())
			if err != nil {
				return nil, fmt.Errorf("failed to get run template manager: %w", err)
			}

			return &appsnapshotCompleter{
				runtemplateManager: obj.(runtemplate.RunTemplateManager),
			}, nil
		},
	})
}

type appsnapshotCompleter struct {
	runtemplateManager runtemplate.RunTemplateManager
}

func (l *appsnapshotCompleter) ID() string {
	return constants.APPSnapshotID.ID()
}
func (l *appsnapshotCompleter) Init(ctx context.Context, opts *workflow.InitInfo) error {
	return nil
}
func (l *appsnapshotCompleter) Create(ctx context.Context, opts *workflow.CreateContext) error {
	if opts == nil {
		return ret.Err(errorcode.ErrorCode_InvalidParamFormat, "opts nil")
	}

	templateID, ok := opts.GetSnapshotTemplateID()
	if !ok {
		return nil
	}

	if opts.IsCreateSnapshot() {
		return nil
	}

	if !opts.IsCubeboxV2() {
		return nil
	}

	ann := opts.ReqInfo.GetAnnotations()
	// The package this Create restores from carries the same kernel／image
	// metadata on its own disk, so it can stand in for the template.
	packageID := strings.TrimSpace(ann[constants.MasterAnnotationRuntimeSnapshotID])
	// Pause snaps are storage catalogs, not Cube run templates, so Resume
	// asks for the original tpl-* first.
	if opts.IsPauseResume() {
		if orig := strings.TrimSpace(ann[constants.MasterAnnotationAppSnapshotTemplateID]); orig != "" {
			templateID = orig
		}
	}

	lrt, err := l.runtemplateManager.EnsureCubeRunTemplate(ctx, templateID)
	if err != nil && packageID != "" && packageID != templateID {
		// Cross-node restore: the template was never built on this node, but
		// the package was just imported here and describes the same VM.
		fallback, fallbackErr := l.runtemplateManager.EnsureCubeRunTemplate(ctx, packageID)
		if fallbackErr == nil {
			log.G(ctx).Infof("run template %s is not on this node; restoring from package %s instead",
				templateID, packageID)
			lrt, err = fallback, nil
		}
	}
	if err != nil {
		return ret.Errorf(errorcode.ErrorCode_InvalidParamFormat, "ensure cube run template %s failed: %v", templateID, err)
	}
	opts.LocalRunTemplate = lrt
	return nil
}

func (l *appsnapshotCompleter) Destroy(ctx context.Context, opts *workflow.DestroyContext) error {
	return nil
}

func (l *appsnapshotCompleter) CleanUp(ctx context.Context, opts *workflow.CleanContext) error {
	return nil
}
