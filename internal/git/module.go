// Copyright 2026 DoorDash, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package git

import "go.uber.org/fx"

// Module provides git adapter implementations to the fx dependency graph.
// Each adapter wraps package-level functions behind port interfaces.
// WorktreeOperator is not provided here — WorktreeManager is constructed
// inline in feature.Module and belongs to the feature lifecycle.
var Module = fx.Module("git",
	fx.Provide(
		func() *PublishAdapter { return &PublishAdapter{} },
		func() *DiffAdapter { return &DiffAdapter{} },
		func() *RebaseAdapter { return &RebaseAdapter{} },
		func() *CrossRefAdapter { return &CrossRefAdapter{} },
		func() *ReviewCommentAdapter { return &ReviewCommentAdapter{} },
		func() *BranchAdapter { return &BranchAdapter{} },
	),
)
