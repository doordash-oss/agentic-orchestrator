# Verification

Agentic uses a fast everyday suite plus explicit extended gates. Run commands
from the repository root. Contributors should run the fast suite before every
handoff, then add the extended tiers that match the area they changed. Current
reference timings come from `docs/testing-baseline.md`.

| Tier or gate | Command | Current wall time | Purpose and when to run |
| --- | --- | --- | --- |
| Fast suite | `make test-fast` | 23s, target <=30s | Everyday confidence check before every handoff. Runs all packages in short mode without the race detector, using the existing `testing.Short` guards. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Builds the binary and checks CLI flags and embedded skill layout. Run when launch behavior, embedded skills, or release packaging may be affected. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Covers lifecycle, state-machine, runs-layout, and protocol-violation behavior. Run when those cross-component paths change. |
| E2E Go (process-launch / API-driven) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Exercises server process launch, API behavior, and session lifecycle with the race detector. Run when those boundaries change. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Extended all-package race and regression sweep. Run before merging high-risk or concurrency-sensitive changes; this is not the everyday unit command. |
| Desktop static checks | `npm run check` | 8s | Runs TypeScript checks, ESLint, Prettier verification, and desktop OpenAPI drift detection. Run for desktop source, tooling, formatting, or API-contract changes. |
| Desktop unit/component/security tests | `npm test && npm run test:security` | 4s | Runs the Vitest renderer, main-process, shared-code, script, and security projects, followed by the explicit security-project gate used by CI. Run for desktop behavior, IPC, preload, policy, or packaging-script changes. |
| Desktop packaged E2E | `npm run package:verify && npm run test:e2e:packaged` | Native CI host | Builds and inspects the native unsigned package, then drives the packaged Electron app and its bundled Go server through the Playwright journey suite. The journeys cover supervision and launch, feature lifecycle and attention flows, planning and review recovery, configuration and workspace restart, history and rewind, publishing and retry behavior, distribution updates, diagnostics, and production security posture. Run for end-to-end desktop workflows, main/server integration, packaging, or distribution behavior. |
| Desktop release audit | `npm run audit:release --workspace desktop && npm run release:verify --workspace desktop` | Local/static plus strict protected-tag mode | Checks dependency licenses and provenance, lockfile and module integrity, desktop protocol and hardened-runtime configuration, and release artifacts. Protected tags additionally require credentials, signing-related artifacts, and strict verification. Run for dependency, packaging, signing, or release-pipeline changes and on protected releases. |
| Go static-analysis gate | `go vet ./...` | Not separately recorded | Required static analysis for all changes; run before push and whenever Go code or generated Go output changes. |
| Go build gate | `go build ./...` | Not separately recorded | Required compilation check for all Go packages; run before push and whenever Go code, generated Go output, or build inputs change. |

`go vet ./...` and `go build ./...` remain required static and build checks.
The race-enabled all-package sweep is the **Race regression** gate, not the
ordinary unit command.

PR descriptions should name the tier(s) run and include a one-sentence reason
for any intentionally skipped relevant tier.
