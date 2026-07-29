# Contextual Verification Input Gates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make verification-triggered `NEED_USER_INPUT` gates explain every blocked check and offer explicit retry or waiver choices without changing generic gate behavior.

**Architecture:** Enrich the harness-owned gate artifact with a sanitized snapshot joined from the testing contract and verification report. Carry that optional structure through OpenAPI and the strict Electron IPC boundary, then render one reusable verification-decision component in both the feature modal and Attention inbox. Existing question answers remain the mutation wire format, so the orchestrator's trusted revision-checked resume semantics do not change.

**Tech Stack:** Go 1.24, YAML gate artifacts, OpenAPI 3.1 with `oapi-codegen` and `openapi-typescript`, Electron, React 19, TypeScript, Zod, Vitest/Testing Library, Playwright.

## Global Constraints

- Every new `*.go`, `*.sh`, or `*.py` source file must begin with the 2026 DoorDash Apache 2.0 notice from `AGENTS.md`.
- Keep `WAIVE` and `RETRY_AFTER_AUTH` as the only supported verification action values.
- Do not expose the testing-contract path, capability probe command, stdout path, stderr path, or run-artifact path through the server API or Electron IPC.
- Preserve the textarea questionnaire for generic gates and gate artifacts written before this feature.
- Persist a selected verification action through the existing gate-draft endpoint and resume through the existing gate-decision endpoint.
- Preserve all unrelated worktree edits; stage only the files named by the task being committed.
- Run **Fast suite** before handoff, plus **E2E smoke shell**, **Isolated integration**, **E2E Go**, `go vet ./...`, and `go build ./...`.

---

## File Structure

- `internal/agent/need_user_input.go`: owns persisted verification gate presentation data and joins contract/report context at synthesis time.
- `internal/agent/need_user_input_test.go`: proves blocker snapshots are accurate, sanitized, ordered, and backward-compatible.
- `internal/agent/implement.go`: supplies the contract and verification report to enriched gate synthesis.
- `api/openapi.yaml`: declares optional verification context on the public gate DTO.
- `internal/server/read_model.go`: maps the sanitized artifact snapshot and trusted allowed actions into the API DTO.
- `internal/server/read_api_contract_test.go`: locks the server response shape and absence of internal paths.
- `internal/server/openapi_contract_test.go`: locks the new OpenAPI component properties.
- `internal/server/serverapi.gen.go`: generated Go OpenAPI types.
- `desktop/src/shared/api/schema.gen.ts`: generated TypeScript OpenAPI types.
- `desktop/src/shared/api/parse.ts`: runtime-validates the new server response.
- `desktop/src/shared/ipc.ts`: defines the strict renderer-safe verification gate shape.
- `desktop/src/main/attention.ts`: maps server snake_case fields to renderer camelCase fields.
- `desktop/src/main/__tests__/attention.test.ts`: proves the mapping and strict validation.
- `desktop/src/renderer/src/features/NeedUserInputVerificationDecision.tsx`: reusable blocker and explicit-choice presentation.
- `desktop/src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx`: focused accessibility and selection tests.
- `desktop/src/renderer/src/features/NeedUserInputModal.tsx`: chooses structured verification UI or legacy questions in feature context.
- `desktop/src/renderer/src/features/NeedUserInputModal.test.tsx`: proves persistence, retry, waiver warning, and legacy fallback.
- `desktop/src/renderer/src/features/AttentionInbox.tsx`: uses the same structured decision inside the global inbox.
- `desktop/src/renderer/src/features/AttentionInbox.test.tsx`: proves consistent inbox behavior.
- `desktop/src/renderer/src/styles/app.css`: styles blocker cards, decision choices, and waiver warning state.
- `desktop/test/e2e/journeys/attention-resolution.spec.ts`: exercises the packaged structured gate and durable retry choice.

---

### Task 1: Persist Sanitized Verification Blocker Context

**Files:**
- Modify: `internal/agent/need_user_input_test.go`
- Modify: `internal/agent/need_user_input.go`
- Modify: `internal/agent/implement.go`
- Modify: `internal/agent/implement_test.go`

**Interfaces:**
- Consumes: `TestingContract`, `TestingContractItem`, `VerificationReport`, and `VerificationCheckResult`.
- Produces:
  - `NeedUserInputVerificationContext`
  - `NeedUserInputVerificationBlocker`
  - `SynthesizeVerificationNeedUserInputGateWithContext(contractPath string, contract *TestingContract, report *VerificationReport, itemIDs []string, iteration int) NeedUserInputRecord`

- [ ] **Step 1: Write a failing synthesis test**

Add a test that creates two contract items and matching blocked report results:

```go
func TestSynthesizeVerificationNeedUserInputGateWithContextExplainsBlockedChecks(t *testing.T) {
	contract := &TestingContract{
		Version: 1,
		Revision: 4,
		Items: []TestingContractItem{
			{
				ID: "deploy", Name: "Deploy smoke test", Repo: "api",
				Command: "make deploy-smoke",
				Capabilities: []TestingContractCapability{{Name: "Okta session", Probe: "okta auth status"}},
			},
			{
				ID: "codesign", Name: "Package signature", Command: "make package-verify",
			},
		},
	}
	report := &VerificationReport{
		ContractRevision: 4,
		Results: []VerificationCheckResult{
			{
				ItemID: "deploy", Status: VerificationStatusBlocked,
				BlockedReason: `missing declared capability "Okta session"`,
			},
			{
				ItemID: "codesign", Status: VerificationStatusBlocked,
				BlockedReason: "host keychain denied access to the signing identity",
			},
		},
	}

	rec := SynthesizeVerificationNeedUserInputGateWithContext(
		"/private/testing-contract.yaml", contract, report,
		[]string{"codesign", "deploy"}, 3,
	)

	if rec.Verification == nil || len(rec.Verification.Blockers) != 2 {
		t.Fatalf("verification context = %+v, want two blockers", rec.Verification)
	}
	if got := rec.Verification.Blockers[0]; got.ItemID != "codesign" ||
		got.Name != "Package signature" ||
		got.Reason != "host keychain denied access to the signing identity" ||
		!strings.Contains(got.Remediation, "environment limitation") {
		t.Fatalf("first blocker = %+v", got)
	}
	if got := rec.Verification.Blockers[1]; got.ItemID != "deploy" ||
		got.RepoName != "api" ||
		got.Command != "make deploy-smoke" ||
		!reflect.DeepEqual(got.Capabilities, []string{"Okta session"}) ||
		!strings.Contains(got.Remediation, "Okta session") {
		t.Fatalf("second blocker = %+v", got)
	}
	if strings.Contains(fmt.Sprintf("%+v", rec.Verification), "okta auth status") ||
		strings.Contains(fmt.Sprintf("%+v", rec.Verification), "/private/") {
		t.Fatalf("verification context leaked a probe or contract path: %+v", rec.Verification)
	}
}
```

Also add a compatibility assertion:

```go
func TestSynthesizeVerificationNeedUserInputGateWithoutContextRemainsLegacyCompatible(t *testing.T) {
	rec := SynthesizeVerificationNeedUserInputGate("/tmp/testing-contract.yaml", 1, []string{"item"}, 1)
	if rec.Verification != nil {
		t.Fatalf("legacy synthesis verification = %+v, want nil", rec.Verification)
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/agent -run 'TestSynthesizeVerificationNeedUserInputGate' -count=1
```

Expected: compile failure because `NeedUserInputRecord.Verification` and `SynthesizeVerificationNeedUserInputGateWithContext` do not exist.

- [ ] **Step 3: Add persisted blocker types and synthesis**

Add these artifact-only types:

```go
type NeedUserInputVerificationContext struct {
	Blockers []NeedUserInputVerificationBlocker `yaml:"blockers"`
}

type NeedUserInputVerificationBlocker struct {
	ItemID       string   `yaml:"item_id"`
	Name         string   `yaml:"name"`
	RepoName     string   `yaml:"repo_name,omitempty"`
	Command      string   `yaml:"command"`
	Reason       string   `yaml:"reason"`
	Capabilities []string `yaml:"capabilities,omitempty"`
	Remediation  string   `yaml:"remediation"`
}
```

Add this field to `NeedUserInputRecord`:

```go
Verification *NeedUserInputVerificationContext `yaml:"verification,omitempty"`
```

Implement `SynthesizeVerificationNeedUserInputGateWithContext` by:

1. Calling the existing `SynthesizeVerificationNeedUserInputGate` with `contract.Revision`.
2. Indexing `contract.Items` and `report.Results` by item ID.
3. Iterating the already-sorted `VerificationDecision.ItemIDs`.
4. Using item ID as the name fallback, an empty command/reason fallback where source data is absent, and only capability `Name` values—never `Probe`.
5. Using `fmt.Sprintf("Make %s available, then retry verification.", strings.Join(capabilities, ", "))` only when the blocked reason contains `missing declared capability`; otherwise use `"Resolve the environment limitation described above, then retry verification."`.
6. Setting `rec.Verification` only when at least one blocker was produced.

Use a small unexported `verificationBlockerRemediation(reason string, capabilities []string) string` helper and `strings.Join` for multiple capabilities.

- [ ] **Step 4: Route production synthesis through the enriched function**

Change the harness pause in `internal/agent/implement.go`:

```go
rec := SynthesizeVerificationNeedUserInputGateWithContext(
	testingContractPath,
	verificationContract,
	harnessVerification.Report,
	harnessVerification.BlockedItems,
	i,
)
```

Extend `TestImplementLoopHarnessCapabilityPauseKeepsSameIteration` to assert:

```go
if rec.Verification == nil || len(rec.Verification.Blockers) != 1 {
	t.Fatalf("gate verification context = %+v, want one blocker", rec.Verification)
}
blocker := rec.Verification.Blockers[0]
if blocker.Name != "Protected" ||
	blocker.Reason != `missing declared capability "Okta session"` ||
	!reflect.DeepEqual(blocker.Capabilities, []string{"Okta session"}) {
	t.Fatalf("gate blocker = %+v", blocker)
}
```

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
go test ./internal/agent -run 'TestSynthesizeVerificationNeedUserInputGate|TestImplementLoopHarnessCapabilityPauseKeepsSameIteration' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit the artifact boundary**

```bash
git add internal/agent/need_user_input.go internal/agent/need_user_input_test.go internal/agent/implement.go internal/agent/implement_test.go
git commit -m "Explain why verification is blocked"
```

---

### Task 2: Expose Verification Context Through OpenAPI

**Files:**
- Modify: `api/openapi.yaml`
- Modify: `internal/server/read_model.go`
- Modify: `internal/server/read_api_contract_test.go`
- Modify: `internal/server/openapi_contract_test.go`
- Regenerate: `internal/server/serverapi.gen.go`
- Regenerate: `desktop/src/shared/api/schema.gen.ts`

**Interfaces:**
- Consumes: `NeedUserInputRecord.Verification` and `NeedUserInputRecord.VerificationDecision.AllowedActions`.
- Produces:
  - OpenAPI `NeedUserInputVerification`
  - OpenAPI `NeedUserInputVerificationBlocker`
  - OpenAPI `NeedUserInputVerificationAction`
  - optional `NeedUserInputGate.verification`

- [ ] **Step 1: Write failing read-model contract assertions**

In `TestNeedUserInputGateDTOsIncludeQuestionnaireAndCycleRouting`, enrich the feature fixture:

```go
Verification: &agent.NeedUserInputVerificationContext{
	Blockers: []agent.NeedUserInputVerificationBlocker{{
		ItemID: "deploy", Name: "Deployment smoke test", RepoName: repoNameSelf,
		Command: "make deploy-smoke",
		Reason: `missing declared capability "Okta session"`,
		Capabilities: []string{"Okta session"},
		Remediation: "Make Okta session available, then retry verification.",
	}},
},
VerificationDecision: &agent.NeedUserVerificationDecision{
	ContractPath: "/private/must-not-leak/testing-contract.yaml",
	ContractRevision: 3,
	ItemIDs: []string{"deploy"},
	AllowedActions: []string{
		agent.NeedUserVerificationWaive,
		agent.NeedUserVerificationRetryAfterAuth,
	},
},
```

Assert the feature detail and `/api/v1/prompts` gate both contain:

```go
verification := detailGate["verification"].(map[string]any)
if !reflect.DeepEqual(verification["allowed_actions"], []any{"WAIVE", "RETRY_AFTER_AUTH"}) {
	t.Fatalf("allowed actions = %+v", verification["allowed_actions"])
}
blocker := verification["blockers"].([]any)[0].(map[string]any)
if blocker["name"] != "Deployment smoke test" ||
	blocker["reason"] != `missing declared capability "Okta session"` ||
	blocker["remediation"] != "Make Okta session available, then retry verification." {
	t.Fatalf("blocker = %+v", blocker)
}
encoded, _ := json.Marshal(detailGate)
for _, forbidden := range []string{"contract_path", "probe", "stdout", "/private/must-not-leak"} {
	if strings.Contains(string(encoded), forbidden) {
		t.Fatalf("gate leaked %q: %s", forbidden, encoded)
	}
}
```

- [ ] **Step 2: Add a failing OpenAPI shape test**

In `internal/server/openapi_contract_test.go`, add:

```go
assertSchemaProperties(t, spec, "NeedUserInputGate", "verification")
assertSchemaProperties(t, spec, "NeedUserInputVerification", "blockers", "allowed_actions")
assertSchemaProperties(
	t, spec, "NeedUserInputVerificationBlocker",
	"item_id", "name", "repo_name", "command", "reason", "capabilities", "remediation",
)
```

- [ ] **Step 3: Run server tests and verify RED**

Run:

```bash
go test ./internal/server -run 'TestNeedUserInputGateDTOsIncludeQuestionnaireAndCycleRouting|TestOpenAPI' -count=1
```

Expected: compile or assertion failure because the DTO and schemas lack `verification`.

- [ ] **Step 4: Define the OpenAPI components**

Add optional `verification` to `NeedUserInputGate`:

```yaml
verification:
  $ref: "#/components/schemas/NeedUserInputVerification"
```

Add:

```yaml
NeedUserInputVerification:
  type: object
  required: [blockers, allowed_actions]
  properties:
    blockers:
      type: array
      maxItems: 100
      items:
        $ref: "#/components/schemas/NeedUserInputVerificationBlocker"
    allowed_actions:
      type: array
      maxItems: 2
      items:
        $ref: "#/components/schemas/NeedUserInputVerificationAction"
NeedUserInputVerificationAction:
  type: string
  enum: [WAIVE, RETRY_AFTER_AUTH]
NeedUserInputVerificationBlocker:
  type: object
  required: [item_id, name, command, reason, capabilities, remediation]
  properties:
    item_id:
      type: string
      x-go-name: ItemID
    name:
      type: string
    repo_name:
      type: string
    command:
      type: string
    reason:
      type: string
    capabilities:
      type: array
      maxItems: 20
      items:
        type: string
    remediation:
      type: string
```

- [ ] **Step 5: Regenerate server and desktop API types**

Run:

```bash
make generate-openapi
npm run generate:api
```

Expected: `internal/server/serverapi.gen.go` and `desktop/src/shared/api/schema.gen.ts` include the two new components and optional gate field.

- [ ] **Step 6: Map only sanitized context in the server read model**

In `needUserInputGateDTO`, set `dto.Verification` only when both the context and trusted decision exist. Copy blockers field-by-field, normalize whitespace, copy capability names, and filter `AllowedActions` to the two exported constants. Do not serialize any other `VerificationDecision` fields.

The resulting core branch should be equivalent to:

```go
if rec.Verification != nil && rec.VerificationDecision != nil && len(rec.Verification.Blockers) > 0 {
	verification := NeedUserInputVerification{
		Blockers: make([]NeedUserInputVerificationBlocker, 0, len(rec.Verification.Blockers)),
	}
	for _, blocker := range rec.Verification.Blockers {
		capabilities := make([]string, 0, len(blocker.Capabilities))
		for _, capability := range blocker.Capabilities {
			if capability = strings.TrimSpace(capability); capability != "" {
				capabilities = append(capabilities, capability)
			}
		}
		verification.Blockers = append(verification.Blockers, NeedUserInputVerificationBlocker{
			ItemID: blocker.ItemID,
			Name: strings.TrimSpace(blocker.Name),
			RepoName: strings.TrimSpace(blocker.RepoName),
			Command: strings.TrimSpace(blocker.Command),
			Reason: strings.TrimSpace(blocker.Reason),
			Capabilities: capabilities,
			Remediation: strings.TrimSpace(blocker.Remediation),
		})
	}
	for _, action := range rec.VerificationDecision.AllowedActions {
		normalized := strings.ToUpper(strings.TrimSpace(action))
		switch normalized {
		case agent.NeedUserVerificationWaive, agent.NeedUserVerificationRetryAfterAuth:
			verification.AllowedActions = append(
				verification.AllowedActions,
				NeedUserInputVerificationAction(normalized),
			)
		}
	}
	dto.Verification = &verification
}
```

The `x-go-name: ItemID` declaration makes the generated field spelling stable.

- [ ] **Step 7: Run focused server checks and verify GREEN**

Run:

```bash
go test ./internal/server -run 'TestNeedUserInputGateDTOsIncludeQuestionnaireAndCycleRouting|TestOpenAPI' -count=1
go test ./internal/server -run TestGeneratedOpenAPIIsCurrent -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit the public API boundary**

```bash
git add api/openapi.yaml internal/server/read_model.go internal/server/read_api_contract_test.go internal/server/openapi_contract_test.go internal/server/serverapi.gen.go desktop/src/shared/api/schema.gen.ts
git commit -m "Carry verification blockers to trusted clients"
```

---

### Task 3: Carry Structured Gates Across Electron IPC

**Files:**
- Modify: `desktop/src/shared/api/parse.ts`
- Modify: `desktop/src/shared/ipc.ts`
- Modify: `desktop/src/main/attention.ts`
- Modify: `desktop/src/main/__tests__/attention.test.ts`

**Interfaces:**
- Consumes: OpenAPI snake_case `NeedUserInputGate.verification`.
- Produces:
  - `AttentionGate.verification`
  - `VerificationGateAction = 'WAIVE' | 'RETRY_AFTER_AUTH'`
  - camelCase blocker properties safe for renderer use.

- [ ] **Step 1: Write a failing main-process mapping test**

Extend the active gate fixture in `desktop/src/main/__tests__/attention.test.ts`:

```ts
verification: {
  blockers: [
    {
      item_id: 'deploy',
      name: 'Deployment smoke test',
      repo_name: 'repo-a',
      command: 'make deploy-smoke',
      reason: 'missing declared capability "Okta session"',
      capabilities: ['Okta session'],
      remediation: 'Make Okta session available, then retry verification.',
    },
  ],
  allowed_actions: ['WAIVE', 'RETRY_AFTER_AUTH'],
},
```

Assert the resulting `gate` item contains:

```ts
verification: {
  blockers: [
    {
      itemId: 'deploy',
      name: 'Deployment smoke test',
      repoName: 'repo-a',
      command: 'make deploy-smoke',
      reason: 'missing declared capability "Okta session"',
      capabilities: ['Okta session'],
      remediation: 'Make Okta session available, then retry verification.',
    },
  ],
  allowedActions: ['WAIVE', 'RETRY_AFTER_AUTH'],
},
```

- [ ] **Step 2: Run the mapping test and verify RED**

Run:

```bash
npm run test --workspace desktop -- src/main/__tests__/attention.test.ts
```

Expected: FAIL because `verification` is absent from the mapped Attention item.

- [ ] **Step 3: Add runtime server parsing**

Define reusable Zod schemas in `desktop/src/shared/api/parse.ts`:

```ts
const ServerNeedUserInputVerificationBlockerSchema = z.object({
  item_id: AttentionIDSchema,
  name: AttentionTextSchema,
  repo_name: z.string().max(500).optional(),
  command: AttentionTextSchema,
  reason: AttentionTextSchema,
  capabilities: z.array(AttentionTextSchema).max(20),
  remediation: AttentionTextSchema,
});

const ServerNeedUserInputVerificationSchema = z.object({
  blockers: z.array(ServerNeedUserInputVerificationBlockerSchema).max(100),
  allowed_actions: z.array(z.enum(['WAIVE', 'RETRY_AFTER_AUTH'])).max(2),
});
```

Add `verification: ServerNeedUserInputVerificationSchema.optional()` to both prompt-snapshot and feature-detail gate schemas. Extend the generated OpenAPI subset assignment near `NeedUserInputGateDTO` so TypeScript compilation proves the parser stays compatible with `schema.gen.ts`.

- [ ] **Step 4: Add the strict renderer IPC shape**

In `desktop/src/shared/ipc.ts`, export:

```ts
export const VerificationGateActionSchema = z.enum(['WAIVE', 'RETRY_AFTER_AUTH']);
export type VerificationGateAction = z.output<typeof VerificationGateActionSchema>;
```

Add this optional field to `AttentionGateSchema`:

```ts
verification: z
  .strictObject({
    blockers: z
      .array(
        z.strictObject({
          itemId: AttentionIDSchema,
          name: AttentionTextSchema,
          repoName: z.string().max(500).optional(),
          command: AttentionTextSchema,
          reason: AttentionTextSchema,
          capabilities: z.array(AttentionTextSchema).max(20),
          remediation: AttentionTextSchema,
        }),
      )
      .max(100),
    allowedActions: z.array(VerificationGateActionSchema).max(2),
  })
  .optional(),
```

- [ ] **Step 5: Map server fields in `AttentionService`**

Add to the gate mapper:

```ts
...(gate.verification === undefined
  ? {}
  : {
      verification: {
        blockers: gate.verification.blockers.map((blocker) => ({
          itemId: blocker.item_id,
          name: blocker.name,
          ...(blocker.repo_name === undefined ? {} : { repoName: blocker.repo_name }),
          command: blocker.command,
          reason: blocker.reason,
          capabilities: blocker.capabilities,
          remediation: blocker.remediation,
        })),
        allowedActions: gate.verification.allowed_actions,
      },
    }),
```

- [ ] **Step 6: Run mapping, type, and API drift checks**

Run:

```bash
npm run test --workspace desktop -- src/main/__tests__/attention.test.ts
npm run typecheck --workspace desktop
npm run check:api-drift --workspace desktop
```

Expected: PASS.

- [ ] **Step 7: Commit the IPC boundary**

```bash
git add desktop/src/shared/api/parse.ts desktop/src/shared/ipc.ts desktop/src/main/attention.ts desktop/src/main/__tests__/attention.test.ts
git commit -m "Preserve verification context across Electron"
```

---

### Task 4: Build the Reusable Explicit Decision UI

**Files:**
- Create: `desktop/src/renderer/src/features/NeedUserInputVerificationDecision.tsx`
- Create: `desktop/src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx`
- Modify: `desktop/src/renderer/src/styles/app.css`

**Interfaces:**
- Consumes: `AttentionGate.verification` and `VerificationGateAction`.
- Produces:
  - `hasStructuredVerificationDecision(item: AttentionGate): boolean`
  - `NeedUserInputVerificationDecision`

- [ ] **Step 1: Write failing component tests**

Create tests using a structured gate and assert:

```tsx
expect(screen.getByRole('heading', { name: 'Deployment smoke test' })).toBeVisible();
expect(screen.getByText('repo-a')).toBeVisible();
expect(screen.getByText('make deploy-smoke')).toBeVisible();
expect(screen.getByText('missing declared capability "Okta session"')).toBeVisible();
expect(screen.getByText('Make Okta session available, then retry verification.')).toBeVisible();
expect(
  screen.getByRole('radio', { name: /I've granted access — retry verification/ }),
).not.toBeChecked();
expect(
  screen.getByRole('radio', { name: /Waive blocked checks and continue/ }),
).not.toBeChecked();
```

Click each radio and assert `onSelect` receives the exact action:

```tsx
await user.click(screen.getByRole('radio', { name: /retry verification/ }));
expect(onSelect).toHaveBeenLastCalledWith('RETRY_AFTER_AUTH');
await user.click(screen.getByRole('radio', { name: /Waive blocked checks/ }));
expect(onSelect).toHaveBeenLastCalledWith('WAIVE');
```

Add a guard test proving a gate with missing blockers, missing actions, or more
than one question returns `false` from `hasStructuredVerificationDecision`.
Unknown action values are rejected earlier by the strict IPC Zod schema.

- [ ] **Step 2: Run the component test and verify RED**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx
```

Expected: module-not-found failure.

- [ ] **Step 3: Implement the focused component**

Use this public surface:

```tsx
export interface NeedUserInputVerificationDecisionProps {
  item: AttentionGate;
  selectedAction: VerificationGateAction | '';
  idPrefix: string;
  onSelect(action: VerificationGateAction): void;
}

export function hasStructuredVerificationDecision(item: AttentionGate): boolean {
  const verification = item.verification;
  return (
    verification !== undefined &&
    verification.blockers.length > 0 &&
    item.questions.length === 1 &&
    verification.allowedActions.length === 2 &&
    verification.allowedActions.includes('RETRY_AFTER_AUTH') &&
    verification.allowedActions.includes('WAIVE')
  );
}
```

Render blocker cards in an accessible section and decisions in a `fieldset` with legend `"How should Agentico continue?"`. Use radio values `RETRY_AFTER_AUTH` and `WAIVE`; set `data-tone="warning"` on the waiver label. The retry description must state that checks rerun from the same iteration. The waiver description must state that checks are recorded as user-authorized waivers and will not run.

- [ ] **Step 4: Add scoped styles**

Add `.need-input-verification__*` rules for:

- a scroll-safe blocker list;
- bordered cards with wrapped `<code>`;
- muted reason/remediation labels;
- two responsive decision cards;
- checked/focus-visible states using `--color-signal`;
- warning choice using `--color-danger`;
- no motion when `prefers-reduced-motion: reduce`.

Reuse existing spacing, radius, type, and color variables; do not introduce new theme tokens.

- [ ] **Step 5: Run component tests and verify GREEN**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx
npm run typecheck --workspace desktop
```

Expected: PASS.

- [ ] **Step 6: Commit the reusable UI**

```bash
git add desktop/src/renderer/src/features/NeedUserInputVerificationDecision.tsx desktop/src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx desktop/src/renderer/src/styles/app.css
git commit -m "Make verification choices explicit"
```

---

### Task 5: Integrate Explicit Decisions in Modal and Inbox

**Files:**
- Modify: `desktop/src/renderer/src/features/NeedUserInputModal.tsx`
- Modify: `desktop/src/renderer/src/features/NeedUserInputModal.test.tsx`
- Modify: `desktop/src/renderer/src/features/AttentionInbox.tsx`
- Modify: `desktop/src/renderer/src/features/AttentionInbox.test.tsx`

**Interfaces:**
- Consumes: `hasStructuredVerificationDecision`, `NeedUserInputVerificationDecision`, and the existing gate draft/resolution APIs.
- Produces: consistent structured verification resolution in both renderer entry points, with unchanged legacy behavior.

- [ ] **Step 1: Add failing modal tests**

Add a structured gate fixture with one question and the Task 3 `verification` payload. Assert:

```tsx
expect(screen.getByRole('dialog', { name: 'Verification needs your input' })).toBeVisible();
expect(screen.getByRole('button', { name: 'Retry verification' })).toBeDisabled();
```

Select retry, assert the draft is immediately persisted as:

```ts
{
  featureId: gate.featureId,
  repoName: 'repo-a',
  cycleType: 'review-comments',
  answers: { [gate.questions[0].prompt]: 'RETRY_AFTER_AUTH' },
}
```

Then assert the enabled `"Retry verification"` button resolves the exact cycle gate with `decision: 'resume'`.

Add a waiver test asserting selection changes the footer label to `"Waive and resume"` and the selected choice has warning tone. Add a legacy test preserving `getByLabelText(/Deployment window/)` and free-text drafting.

- [ ] **Step 2: Add failing inbox tests**

Add a `gateItem` fixture to `AttentionInbox.test.tsx`. Open the item, assert blocker context is visible, select retry, and assert `window.agentico.saveGateDraft` receives `RETRY_AFTER_AUTH`. Click `"Retry verification"` and assert `resolveGate` uses the feature/repo/cycle routing.

Add one assertion that a legacy gate still renders a textarea and `"Resume"`.

- [ ] **Step 3: Run renderer tests and verify RED**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/features/NeedUserInputModal.test.tsx src/renderer/src/features/AttentionInbox.test.tsx
```

Expected: failures because both parents still render textareas.

- [ ] **Step 4: Integrate the modal**

Derive:

```ts
const structuredVerification = hasStructuredVerificationDecision(item);
const verificationQuestion = structuredVerification ? item.questions[0] : undefined;
const selectedVerificationAction =
  verificationQuestion === undefined
    ? ''
    : ((draft[verificationQuestion.index] ?? '') as VerificationGateAction | '');
```

For structured gates:

- title the dialog `"Verification needs your input"`;
- summarize `${item.verification!.blockers.length} required check(s) could not run.`;
- render `NeedUserInputVerificationDecision`;
- on selection, create the next draft, update `draftRef.current` synchronously, update React state, and call `saveDraft` with that explicit next draft;
- treat the gate as complete only when the selected value is one of the supported actions;
- label the footer `"Retry verification"` or `"Waive and resume"`; use the existing disabled state until selection.

Change `saveDraft` to accept optional item/draft arguments so immediate selection persistence cannot read stale React state:

```ts
const saveDraft = useCallback(
  async (
    currentItem = itemRef.current,
    currentDraft = draftRef.current,
  ): Promise<void> => {
    // existing request construction
  },
  [],
);
```

Keep the current header, textarea loop, hint, and `"Resume agent"` label for legacy gates.

- [ ] **Step 5: Integrate the Attention inbox**

In the gate branch:

- derive the same structured/selected state;
- render `NeedUserInputVerificationDecision` instead of textareas when valid;
- on selection, build `nextDraft`, update `setDrafts`, and immediately call `saveGateDraftForItem(item, nextDraft)` through the existing `saveDraft` notice wrapper;
- use `"Retry verification"` or `"Waive and resume"` for the primary button;
- retain `"Resume"` and textareas for legacy gates;
- retain abort behavior for both gate kinds.

- [ ] **Step 6: Run renderer tests and verify GREEN**

Run:

```bash
npm run test --workspace desktop -- src/renderer/src/features/NeedUserInputVerificationDecision.test.tsx src/renderer/src/features/NeedUserInputModal.test.tsx src/renderer/src/features/AttentionInbox.test.tsx
npm run typecheck --workspace desktop
```

Expected: PASS.

- [ ] **Step 7: Commit both renderer entry points**

```bash
git add desktop/src/renderer/src/features/NeedUserInputModal.tsx desktop/src/renderer/src/features/NeedUserInputModal.test.tsx desktop/src/renderer/src/features/AttentionInbox.tsx desktop/src/renderer/src/features/AttentionInbox.test.tsx
git commit -m "Put blocker context at every resolution point"
```

---

### Task 6: Prove Durable Packaged Resolution

**Files:**
- Modify: `desktop/test/e2e/journeys/attention-resolution.spec.ts`

**Interfaces:**
- Consumes: structured gate YAML, packaged Attention inbox, existing draft and resolution endpoints.
- Produces: packaged regression coverage for explanatory context and explicit retry choice across relaunch.

- [ ] **Step 1: Update the seeded gate with structured context**

Add this block to `seedVerificationNeedUserInputGate`:

```yaml
verification:
  blockers:
    - item_id: deployment-capability
      name: Deployment smoke test
      repo_name: gate-lab
      command: make deploy-smoke
      reason: missing declared capability "deployment credentials"
      capabilities:
        - deployment credentials
      remediation: Make deployment credentials available, then retry verification.
```

Keep the existing trusted `verification_decision` block and question prompt so the mutation protocol remains real.

- [ ] **Step 2: Replace magic-string UI actions with explicit-choice assertions**

In the packaged gate journey:

1. Assert `"Deployment smoke test"`, `"make deploy-smoke"`, the missing-credentials reason, and remediation are visible.
2. Assert `"Retry verification"` is disabled.
3. Click the radio named `/I've granted access — retry verification/`.
4. Wait for the gate YAML to contain `answer: RETRY_AFTER_AUTH`.
5. Relaunch and assert the retry radio remains checked.
6. Resolve using the `"Retry verification"` button.

Do not alter generic cycle-gate fixtures; they continue to cover textarea fallback.

- [ ] **Step 3: Run the packaged journey**

Run:

```bash
npm run test:e2e:packaged --workspace desktop -- test/e2e/journeys/attention-resolution.spec.ts
```

Expected: PASS with the structured choice persisted across app relaunch and the real bundled server resuming the same gate.

- [ ] **Step 4: Commit packaged coverage**

```bash
git add desktop/test/e2e/journeys/attention-resolution.spec.ts
git commit -m "Protect contextual gate resolution end to end"
```

---

### Task 7: Full Verification and Handoff Evidence

**Files:**
- Modify only if a verification command reveals a defect in the files owned by Tasks 1–6.

**Interfaces:**
- Consumes: all completed tasks.
- Produces: verification evidence named by repository tier.

- [ ] **Step 1: Run desktop unit, static, and build checks**

Run:

```bash
npm run test --workspace desktop
npm run check --workspace desktop
npm run build --workspace desktop
```

Expected: PASS.

- [ ] **Step 2: Run Go static and build checks**

Run:

```bash
go vet ./...
go build ./...
```

Expected: PASS.

- [ ] **Step 3: Run Fast suite**

Run:

```bash
make test-fast
```

Expected: PASS. Record the tier name **Fast suite** in the handoff.

- [ ] **Step 4: Run launch and lifecycle extended gates**

Run:

```bash
bash test/e2e/smoke.sh
go test ./test/integration/... -count=1
go test ./test/e2e/... -count=1 -race
```

Expected: PASS. Record the tier names **E2E smoke shell**, **Isolated integration**, and **E2E Go** in the handoff.

- [ ] **Step 5: Verify generated files and worktree scope**

Run:

```bash
go test ./internal/server -run TestGeneratedOpenAPIIsCurrent -count=1
npm run check:api-drift --workspace desktop
git diff --check
git status --short
```

Expected: generated API checks pass, no whitespace errors, and only pre-existing unrelated user changes remain uncommitted.

- [ ] **Step 6: Resolve any verification failure at its owning task**

If Steps 1–5 fail, return to the task that owns the failing file, add a focused
regression test, verify it fails for the observed reason, implement the minimal
correction, rerun that task's checks, and use that task's exact `git add` list.
If all checks pass, make no additional commit.

- [ ] **Step 7: Prepare the handoff**

Report:

- blocker context now shown in modal and inbox;
- explicit retry and waiver controls;
- legacy questionnaire fallback;
- commits created by each task;
- **Fast suite**, **E2E smoke shell**, **Isolated integration**, and **E2E Go** results;
- `go vet ./...`, `go build ./...`, desktop test/check/build, and packaged journey results;
- **Race regression skipped:** no concurrency-sensitive behavior was introduced.
