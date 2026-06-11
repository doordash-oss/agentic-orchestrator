# Agentic Orchestrator

### 'Na sola botta â luna — e po' diece vote 'nsieme.

Agentic Orchestrator è 'nu orchestratore 'e workflow 'e sviluppo AI ca trasfòrma cualsiasi ingegnere 'n'nu moltiplicatore 'e forze. Descrive 'e tue feature, piglie 'e decisione 'e alto livello, e 'o AI fa 'o rimanente — ricerca, pianificazione, implementazione, code review, pull request — tutto ca gira 'nzieme 'a 'nu solo terminale.

> 'O CLI locale è `agentico`

<img width="3000" height="1800" alt="agentico-basic-flow-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Pecché Agentic Orchestrator?

'A parte difficile d''o coding agentico nun è dicere a 'nu modello 'e cambiare 'e file. 'A parte difficile è passare 'a 'na richiesta vaga 'e alto livello a 'na PR revisionabile senza perdere 'o contesto, saltare 'o lavoro 'e design, o lassare ca 'nu piano sbagliato produca 'na diff enorme. Senza gestione, è accussì ca 'e squadre si ritrovano c''o AI slop: codice ca pare giusto ma viene prodotto cchiù veloce d''o contesto, d''e test e d''o processo 'e revisione necessari pè rennere 'o codice affidabile. Agentic Orchestrator è costruito attorno a chisto problema: trasfòrma 'nu solo prompt 'e feature 'n'nu workflow ingegneristico duraturo ca raccoglie contesto, fa domande, progetta l'approccio, decompone 'o lavoro, l'implementa, 'o verifica, 'o rivede e 'o pubblica.

Chisto è 'o valore vero d''o "oneshot": 'nu ingegnere po' descrivere 'na feature grossa 'na sola vota, e po' supervisionare 'e checkpoint addò conta 'o giudizio invece 'e guidare a mano ogni prompt, sessione d''o terminale, worktree, test run, revisione e passo 'e PR.

- **'O contesto se costruisce, nun se spera** — 'E feature Large e Moonshot partono costruenno 'na knowledge base pe repo, e po' eseguono fasi 'e inquiry, ricerca e design prima 'e pianificare. L'agente 'e implementazione legge artefatti strutturati invece 'e basarsi 'ncopp'a 'na sola chat history sovraccaricata.
- **'A complessità è suddivisa 'n fasi** — 'A pianificazione produce 'nu roadmap, e po' ogni fase d''o roadmap tene 'o proprio piano dettagliato. 'Na fase tracer-bullet stabilisce 'a strada; 'e fasi TDD successive ritirano 'e stub e ampliano 'a copertura.
- **'E controlli 'e qualità vengono prima ca 'a diff diventa costosa** — 'E validatori 'e piano revisionano architettura, scope, struttura, e, pe lavori ad alto rischio, sicurezza, performance e testing. 'E loop 'e implementazione e 'e Final Review usano prove 'e verifica esplicite prima ca 'a feature diventa pubblicabile.
- **L'attenzione umana è riservata â decisioni** — Gate opzionali si fermano pe decisioni 'e inquiry, ricerca, design, piano, user-input e publish. Appruove 'a direzione, chiedi iterazione, o rispunne a domande mirate; l'orchestratore tene 'o stato d''o workflow.
- **'O parallelismo è 'o moltiplicatore, nun 'a premessa** — Pecché ogni feature ha worktree, branch, sessioni e artefatti isolati, puoi gestire cchiù workflow complessi 'nsieme senza mescolare stati o bloccare 'o tuo checkout principale.
- **L'orchestrazione d''e provider è esplicita** — Uno solo provider basta pe eseguire l'intero workflow; aggiungine 'nu secondo pe dividere 'o lavoro. 'E default Claude gestisce raccolta d''o contesto, pianificazione e implementazione mentre Codex fa 'a revisione indipendente, ma 'e modelli possono essere sovrascritti pe fase e scambiati a runtime. Usa `--providers` pè limitare l'orchestratore ai CLI ca haie davero installato.

'O design segue 'e pattern descritti nell'articolo [Costruire Agenti Efficaci](https://www.anthropic.com/engineering/building-effective-agents) 'e Anthropic: prompt chaining, parallelizzazione, orchestrator-workers e loop evaluator-optimizer. Codifica pure 'o workflow [esplora → pianifica → codifica](https://code.claude.com/docs/en/best-practices) 'e Claude Code e 'a guida 'e OpenAI sull'agente [orchestrazione e guardrail](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/).

## 'A Partenza

Usa Homebrew si 'o tiene già; sinnò pigliate 'o binario precompilato. Costruisce dall'origine sulo si staje lavoranno 'ncopp'a agentico stesso.

**Homebrew** (consigliato — macOS/Linux):

```bash
brew install doordash-oss/agentic-orchestrator/agentico
```

**Binario precompilato** — senza Homebrew o Go (macOS/Linux, amd64/arm64):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/doordash-oss/agentic-orchestrator/releases/latest | sed 's@.*/@@')
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/doordash-oss/agentic-orchestrator/releases/download/${TAG}/agentic-orchestrator_${TAG#v}_${OS}_${ARCH}.tar.gz" | tar -xz -C ~/.local/bin agentico
# assicurate ca ~/.local/bin sia 'ncopp'ô vostro PATH
```

**Dall'origine** — pe chi contribuisce a agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# or: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Po' avviate `agentico`. Aggiornate quann' vulite c''o `agentico update` — usa 'o metodo giusto pe come l'haie installato.

'A prima vota ca 'o fate partire, Agentic Orchestrator v'accompagna attreverso 'nu flusso 'e benvenuto pe scegliere 'e directory d''o workspace. Doppo, site 'ncopp'ô dashboard.

**Tre tasti ca nun v'abbandonate**: `n` ('na nova feature), `?` (aiuto), `a` (guarda 'o lavoro attivo; rispunne, approva, o rivedi quann' te 'o chiede). Tutto 'o rimanente 'o truvate 'a l'overlay d'aiuto.

<a id="prerequisites"></a>
<a name="prerequisites"></a>
## Prerequisiti

### Obbligatori

| Tool | Purpose | Install |
|------|---------|---------|
| **`git`** | Operazioni 'e worktree, branch, commit e rebase | Pre-installato 'ncopp'a 'a maggior parte d''e sistemi |
| **`gh` CLI** | Creazione 'e PR al push e aggiornamento d''o corpo 'e PR cross-repo durante 'o Publish | [GitHub CLI docs](https://docs.github.com/en/github-cli/github-cli), e po' `gh auth login` |

### Provider CLI — Installane Almeno Uno

Agentic Orchestrator tene bisogno 'e **almeno uno** AI provider CLI.

| Tool | Role | Install |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend predefinito pe KB, inquiry, ricerca, design, pianificazione, implementazione e chat | [Claude Code setup](https://code.claude.com/docs/en/getting-started) or `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend predefinito pe 'a Final Review e 'e modelli 'e revisione supportati 'a Codex | [Codex CLI setup](https://developers.openai.com/codex/cli) or `npm i -g @openai/codex@latest` |

### Opzionali

| Tool | Purpose | Install |
|------|---------|---------|
| **Go 1.25+** | Serve sulo pe costruire `agentico` dall'origine — nun è necessario quann' usi 'nu [binario precompilato](#a-partenza) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ and npm** | Serve sulo quann' stai installando Claude Code o Codex tramite npm | [nodejs.org](https://nodejs.org/) |

Doppo 'e installà 'o/i provider CLI, esegui `claude auth status` e/o `codex login status`, cchiù `gh auth status`, prima 'e lanciare `agentico`.

## How It Works

### The Feature Lifecycle

The lifecycle is profile-dependent and checkpoint-driven. Medium starts at planning. Large and Moonshot first build context, clarify intent, and explore design options. All profiles then enter the roadmap loop: create a roadmap, plan one roadmap phase at a time, implement it, commit phase anchors, and continue until the final phase reaches Final Review.

<img width="1051" height="570" alt="image" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Knowledge Base Build** — Builds or refreshes a per-repo knowledge base covering architecture, conventions, API surface, dependencies, and verification. Fresh KBs are reused and the phase is skipped.

**Inquire, Research, Design** — Turns a high-level request into explicit answers, research findings, and a design direction. Q&A artifacts are persisted and fed forward so later phases do not depend on memory alone.

**Roadmap and Phase Planning** — Creates the top-level roadmap, then a detailed plan for each roadmap phase. Large and Moonshot run plan validators; Medium skips plan critics for lower overhead.

**Implementation** — Runs a unified phase implementation loop across the phase-scoped repo set. Medium and Large rely on Final Review; Moonshot also keeps per-iteration review during implementation.

**Final Review** — Runs once after the last roadmap phase, across every touched repo that has not already been published. The phase contains its own review/fix loop. Passing Final Review moves the feature to `CodeReady`; exhausting the loop or violating the phase contract fails the feature.

**Publishing** — If auto-publish is enabled, Agentic Orchestrator commits, rebases, pushes, creates PRs, and injects cross-repo PR links automatically. If manual publish is enabled, the TUI pauses at `CodeReady` so you can review the diff and PR description first.

### Pipeline Profiles

When creating a feature, choose a pipeline depth:

| Profile | Phases | Best for |
|---------|--------|----------|
| **Medium** | Roadmap plan → per-phase plan/implement loop → Final Review → Publish | Small, well-understood changes where you already know the approach |
| **Large** | KB → Inquire → Research → Design → roadmap loop → Final Review → Publish | Most complex features (default) |
| **Moonshot** | Same phase sequence as Large, with max effort, plan-review defaults, and per-iteration implementation review | High-risk or highly ambiguous changes |

### Worktree Isolation

Each feature runs in its own git worktree under `~/.agentic-orchestrator/worktrees/` (legacy installs continue to use `~/.agentic-workflow/worktrees/` until you opt in). This means:
- Multiple features can work on the same repo simultaneously
- No branch conflicts between concurrent features
- Your main working copy stays untouched
- Worktrees are cleaned up with `c` after completion

### Multiple Repositories

Every feature targets one or more repositories with the same lifecycle and state machine. When a feature spans more than one repo, Agentic Orchestrator:
- Creates worktrees in each target repo
- Builds an execution plan with dependency ordering across repos
- Runs implementation per-repo (sequentially or in parallel based on dependencies)
- Cross-references PRs across repos automatically

When a feature targets a single repo, the per-repo Repo Progress panel, the cycle-selector modal, and the cross-reference PR table collapse — the rest of the lifecycle is identical.

### Knowledge Base

Before diving into a feature, Agentic Orchestrator can build a per-repo knowledge base — a structured document graph covering architecture, conventions, API surface, dependencies, and verification methods. The KB is cached and incrementally updated (only when HEAD changes), so subsequent features in the same repo start faster.

### Plan Validation Gate

Plans are reviewed by specialized AI critics before implementation begins:

| Critic | Focus | When Active |
|--------|-------|-------------|
| **Architecture** | Roadmap-level pattern consistency, module boundaries, dependency direction | Large/Moonshot, all risk levels |
| **Structural** | Phase-plan completeness, required sections, executable task shape | Large/Moonshot, all risk levels |
| **Scope** | Requirement coverage, phase sizing, over-engineering detection | Large/Moonshot, all risk levels |
| **Security** | Auth, injection, data protection calibrated to project context | Large/Moonshot, high risk |
| **Performance** | Scalability, query efficiency, resource management | Large/Moonshot, high risk |
| **Testing** | Coverage adequacy, edge cases, regression protection | Large/Moonshot phase plans, high risk |

Critics run in parallel and produce independent verdicts. If any critic requests changes, the plan is revised and re-validated automatically. Medium skips plan critics but still runs Final Review before publish.

## Usage

### TUI Dashboard

Launch with `agentico`. The dashboard shows all features organized by status:

- **In Progress** — actively being worked on (researching, planning, implementing)
- **Published** — PR created, awaiting merge
- **Completed** — marked as done

Features needing your attention (pending permissions, help requests) show a warning indicator.

### Creating a Feature

Press `n` from the dashboard to open the wizard:

1. **What** — Name and describe the feature. Supports pasting images (`Ctrl+V`) and attaching files (`@`).
2. **Where** — Select target repo(s). Browse for new directories or create repos on the fly.
3. **Pipeline** — Choose Medium, Large, or Moonshot. Toggle individual checkpoints (inquiry review, research review, design review, plan review, manual publish).
4. **Review** — Adjust risk level, models per phase, exit criteria. Submit to start.

### Interacting with Agents

**Watch** (`a`) — Open active live work in real time. The same key becomes **Answer**, **Approve**, or **Review** when the agent needs input.

**Overview** (`o`) — Switch the dashboard right panel from Live Preview to the detailed overview. Press `l` from Overview to return to Live Preview; outside Overview, `l` still opens logs.

**Stop watching** (`Esc/Ctrl+]`) — Return to the dashboard. The agent keeps running.

### Post-Implementation Actions

Once a feature reaches code-ready or published state:

| Key | Action |
|-----|--------|
| `p` | Publish as PR (diff review → commit log → PR description → confirm) |
| `t` | Tweak — make a targeted change without re-running the full pipeline |
| `Shift+F` | Refactor — apply a refactoring prompt to the implementation |
| `b` | Rebase on main |
| `g` | View and resolve PR review comments |
| `D` | Mark as done |

### Ask Me Anything

Press `/` anywhere to open the built-in AI chat. It's a read-only Claude session that can explain how Agentic Orchestrator works, debug issues by reading feature logs and artifacts, search the codebase, and answer questions — without modifying any files.

### Keybindings

> For the complete reference, see [docs/keybindings.md](docs/keybindings.md).

## Configuration

Config lives at `~/.agentic-orchestrator/config.yaml` (auto-created on first launch). If a legacy `~/.agentic-workflow/` directory already exists, it is reused in place so existing installs keep working without a manual copy.

```yaml
defaults:
  models:
    research: "opus[1m]"     # Model for research phase
    planning: "opus[1m]"     # Model for planning phase
    implementation: "opus[1m]" # Model for implementation phase
    review: gpt-5.4          # Model for review phase (Codex)
    utilities: sonnet        # Model for chat and utility tasks
    kb_build: "opus[1m]"     # Model for knowledge base builds
  exit_criteria: |
    - Feature fully implemented per plan
    - Unit tests added/updated as needed
    - Integration tests added/updated as needed
    - Code formatted per project standards
    - Relevant tests pass
    - No linting errors
  max_iterations: 10
  max_consecutive_failures: 3
  max_consecutive_no_progress: 3
  inquireness: high          # How often planning questions are surfaced
  pipeline: large            # Default pipeline (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Scanned for git repos on startup
```

### Model Overrides

Each feature can override default models during creation via the wizard (step 4). Models can be specified with explicit provider prefixes (e.g., `claude:opus`, `codex:gpt-5.4`) or as bare names that are automatically routed to the best-matching provider.

### Launch Flags

```text
agentico [flags]

Flags:
  --config <path>                  Config file (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory (default: ~/.agentic-orchestrator/features)
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --providers <list>               Restrict to specific providers (claude,codex)
  --help, -h                       Show help
  --version, -v                    Show version
```

### Updating

```text
agentico update [--check|-n]
```

Run `agentico update` to upgrade to the latest stable release. Use
`agentico update --check` (alias `-n`) to report the current and latest
available versions without installing anything; it exits `0` and prints an
already-up-to-date message when you are on the newest release.

## Development

```bash
# Build
go build -o bin/agentico ./cmd/agentico

# Or use the make target (writes ./bin/agentico)
make build

# Everyday verification
make test-fast

# Generate keybinding docs
go generate ./internal/tui/...
```

Verification is split into named tiers so everyday checks stay fast while
extended coverage remains available.

| Tier | Command | Current wall time | Purpose |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Everyday all-package short-mode check before handoff. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Builds the binary and checks CLI flags plus embedded skill layout. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Lifecycle, state-machine, and protocol-violation coverage. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Full TUI and teatest behavior with the race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Observer-backed TUI event and feature-span integration coverage. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Extended all-package race/regression sweep. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Live skill/guideline discovery against real LLM CLIs. |

`go vet ./...` and `go build ./...` remain required static and build checks.
The tagged **TUI observability** tier is the explicit opt-in gate for slower
observer-backed TUI integration coverage. The race-enabled all-package sweep is
the **Race regression** tier, not the ordinary unit command. See
[AGENTS.md](AGENTS.md) and
[docs/testing-baseline.md](docs/testing-baseline.md) for timing details, and
see AGENTS.md for the isolated-run pattern for running a second instance without
colliding with the first.

## Contributing

Pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, branch and commit conventions.

Contributions to this project require agreeing to the DoorDash Contributor License Agreement.
See [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## License

Agentic Orchestrator is licensed under the [Apache License, Version 2.0](LICENSE.txt).

## Notices

See [NOTICE.txt](NOTICE.txt) for third-party components and attributions.
