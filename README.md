# Agentic Orchestrator

### Affruntati lu moonshot cu un sulu colpu — poi rifatilu deci voti in parallelu.

Agentic Orchestrator è n'orchestraturi di workflow di sviluppu AI chi fa divintari ogni ngigneri un multiplicaturi di forza. Discriviti li vostri feature, pigghiati li dicisioni d'altu liveddu, e l'AI tratta lu restu — research, planning, implementation, code review, pull request — tuttu n'esicuzzioni cuncurrenti di nu sulu terminali.

> La CLI lucali è `agentico`

<img width="3000" height="1800" alt="agentico-basic-flow-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Picchì Agentic Orchestrator?

La parti difficili dû coding agentic nun è addumannari a nu mudellu di canciari file. La parti difficili è passari di na richiesta di feature vaga e d'altu liveddu a na PR rivisàbbili senza pèrdiri cuntestu, senza satari lu travagghiu di disignu, e senza lassari chi un pianu debuli produca nu diff granni. Senza guvernu, accussì li team ricèvinu risultati AI scadenti: còdici ca pari plausìbbili, pruduciutu cchiù prestu dû cuntestu, dî test e dû prucessu di review nicissariu pi fidàrisi. Agentic Orchestrator è custruitu attornu a stu prubblema: trasforma un prompt di feature nta nu workflow di ngignirìa duràbbili chi raccogghi cuntestu, fa dumanni, disigna l'approcciu, scumpuni lu travagghiu, lu implementa, lu virìfica, lu rivedi e lu pubblica.

Chistu è lu veru valuri "oneshot": nu ngigneri pò discrìviri na feature granni na vota sula, poi survigghiari li checkpoint unni lu giudizziu cunta, mmeci di accumpagnari a manu ogni prompt, sessioni di terminali, worktree, test run, passaggiu di review e passu di PR.

- **Lu cuntestu si custruisci, nun si spera** — Li feature Large e Moonshot accuminzanu custruennu na knowledge base pi ogni repo, poi esèguinu li fasi inquiry, research e design prima dû planning. L'agenti d'implementation leggi artifacts strutturati mmeci di fidàrisi a na sula chat history carricata troppu.
- **La cumplissità è spartuta pi fasi** — Lu planning pruduci na roadmap, poi ogni fasi dâ roadmap havi lu so phase plan dittagghiatu. Na fasi tracer-bullet stabbilisci lu caminu; li fasi TDD fill-in successivi ritìranu li stub e allàrganu la cupertura.
- **Li quality gate arrivanu prima chi lu diff addiventa caru** — Li plan validator rìvidinu architecture, scope, structure e, pi travagghiu ad autu risicu, security, performance e testing. Li loop di Implementation e Final Review ùsanu evidenza esplicita di virìfica prima chi la feature addiventa pubblicàbbili.
- **L'attinzioni umana resta pi li dicisioni** — Li gate opzziunali si fèrmanu supra dicisioni di inquiry, research, design, plan, user-input e publish. Vui appruvati la direzzioni, addumannati n'itirazzioni, o arrispunniti a dumanni mirati; l'orchestraturi manteni lu statu dû workflow.
- **Lu parallilismu è lu multiplicaturi, non la premissa** — Siccomu ogni feature havi worktree, branch, sessioni e artifacts isolati, putiti fari curriri cchiù workflow cumplessi a na vota senza ammiscari statu o bluccari lu checkout principali.
- **L'orchestrazzioni dî provider è esplicita** — Un provider basta pi eseguiri tuttu lu workflow; agghiuncìtini nu secunnu pi spartiri lu travagghiu. Di default Claude tratta la raccolta dû cuntestu, lu planning e l'implementation, mentri Codex tratta la review indipinnenti; li mudelli però ponnu èssiri suvrascritti pi fasi e scanciati a runtime. Usati `--providers` pi limitari l'orchestraturi ê CLI chi aviti davveru nstallati.

Lu disignu segui li pattern discritti ntô articulu [Building Effective Agents](https://www.anthropic.com/engineering/building-effective-agents) di Anthropic: prompt chaining, parallelization, orchestrator-workers e loop evaluator-optimizer. Codifica macari lu workflow [explore → plan → code](https://code.claude.com/docs/en/best-practices) di Claude Code e la guida di OpenAI supra [orchestration and guardrails](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/) pi agenti.

<a id="quick-start"></a>

## Partenza viloci

Usati Homebrew siddu l'aviti; sinnò pigghiati lu binariu già custruitu. Custruiti dû còdici surgenti sulu siddu stati travagghiannu supra agentico stissu.

**Homebrew** (cunsigghiatu — macOS/Linux):

```bash
brew install doordash-oss/agentic-orchestrator/agentico
```

**Binariu precompilatu** — senza Homebrew o Go (macOS/Linux, amd64/arm64):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/doordash-oss/agentic-orchestrator/releases/latest | sed 's@.*/@@')
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/doordash-oss/agentic-orchestrator/releases/download/${TAG}/agentic-orchestrator_${TAG#v}_${OS}_${ARCH}.tar.gz" | tar -xz -C ~/.local/bin agentico
# assicurativi chi ~/.local/bin è ntô vostru PATH
```

**Dû sorgenti** — pi cuntribbuiri a agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# or: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Poi eseguiti `agentico`. Putiti attualizzari quannu vuliti cu `agentico update` — usa lu mètudu giustu secunnu comu lu nstallàstivu.

Ô primu avviu, Agentic Orchestrator vi guida cu nu flussu di binvinutu pi scègghiri li vostri carteddi di workspace. Doppu chistu, arrivati â dashboard.

**Tri tasti di ricurdari**: `n` (new feature), `?` (help), `a` (watch active work; answer, approve, or review when prompted). Tuttu lu restu si trova dâ finestra di help.

<a id="prerequisites"></a>

## Riquisiti

### Nicissari

| Strumentu | Scopu | Nstallazzioni |
|------|---------|---------|
| **`git`** | Operazzioni di worktree, branch, commit e rebase | Prisenti di sòlitu nta la maiurìa dî sistemi |
| **`gh` CLI** | Criazzioni dî PR ô mumentu dû push e attualizzazzioni cross-repo dû corpu dâ PR duranti Publish | [GitHub CLI docs](https://docs.github.com/en/github-cli/github-cli), poi `gh auth login` |

### CLI di provider — nstallàtini armenu unu

Agentic Orchestrator havi bisognu di **armenu una** CLI di provider AI.

| Strumentu | Rolu | Nstallazzioni |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend predefinitu pi KB, inquiry, research, design, planning, implementation e chat | [Claude Code setup](https://code.claude.com/docs/en/getting-started) o `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend predefinitu pi Final Review e pi mudelli di review appujati supra Codex | [Codex CLI setup](https://developers.openai.com/codex/cli) o `npm i -g @openai/codex@latest` |

### Opzziunali

| Strumentu | Scopu | Nstallazzioni |
|------|---------|---------|
| **Go 1.25+** | Nicissariu sulu pi custruiri `agentico` dû còdici surgenti — nun è nicissariu quannu si usa un [prebuilt release binary](#quick-start) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ e npm** | Nicissariu sulu quannu si nstalla Claude Code o Codex attraversu npm | [nodejs.org](https://nodejs.org/) |

Doppu aviri nstallatu li vostri CLI di provider, eseguiti `claude auth status` e/o `codex login status`, cchiù `gh auth status`, prima d'avviari `agentico`.

## Comu Funziona

### Lu Ciclu di Vita dâ Feature

Lu lifecycle dipenni dû profile ed è guidatu di checkpoint. Medium accumincia dû planning. Large e Moonshot prima custruìscinu cuntestu, chiarìscinu l'intenzioni, ed esplòranu opzziuni di design. Tutti li profile poi trasinu ntô loop dâ roadmap: criari na roadmap, pianificari na fasi dâ roadmap a la vota, implementàrila, cummittari l'ancuri di fasi, e cuntinuari finu a quannu la fasi finali arriva a Final Review.

<img width="1051" height="570" alt="image" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Knowledge Base Build** — Custruisci o attualizza na knowledge base pi ogni repo, cu architecture, conventions, API surface, dependencies e verification. Li KB frisci sunnu riusati e la fasi veni satata.

**Inquire, Research, Design** — Trasforma na richiesta d'altu liveddu nta risposti espliciti, findings di research e na direzzioni di design. L'artifacts Q&A vennu persistuti e passati avanti, accussì li fasi successivi nun dipènninu sulu dâ mimoria.

**Roadmap and Phase Planning** — Cria la roadmap principali, poi nu pianu dittagghiatu pi ogni fasi dâ roadmap. Large e Moonshot esèguinu li plan validator; Medium sata li plan critic pi mantèniri cchiù vasciu l'overhead.

**Implementation** — Esegui nu loop unificatu d'implementation di fasi supra lu nzemi di repo ntô scope dâ fasi. Medium e Large s'appòggianu a Final Review; Moonshot manteni macari review pi ogni itirazzioni duranti l'implementation.

**Final Review** — Gira na vota doppu l'ùrtima fasi dâ roadmap, supra ogni repo tuccatu chi nun fu già pubblicatu. La fasi cunteni lu so loop review/fix. Passari Final Review porta la feature a `CodeReady`; esauriri lu loop o viulari lu cuntrattu dâ fasi fa falliri la feature.

**Publishing** — Siddu auto-publish è abbilitatu, Agentic Orchestrator fa commit, rebase, push, cria PR e nzirisci li link PR cross-repo autumaticamenti. Siddu manual publish è abbilitatu, la TUI si ferma a `CodeReady` accussì putiti rivediri prima lu diff e la discrizzioni dâ PR.

### Profile di Pipeline

Quannu criati na feature, scigghiti la prufunnità dâ pipeline:

| Profile | Fasi | Cchiù adattu pi |
|---------|--------|----------|
| **Medium** | Roadmap plan → loop plan/implement pi fasi → Final Review → Publish | Canciamenti nicareddi e chiari, unni sapiti già l'approcciu |
| **Large** | KB → Inquire → Research → Design → loop dâ roadmap → Final Review → Publish | La maiurìa dî feature cumplessi (default) |
| **Moonshot** | Stissa sequenza di fasi di Large, cu max effort, default di plan-review e review d'implementation pi ogni itirazzioni | Canciamenti ad autu risicu o assai ambigui |

### Isulamentu dî Worktree

Ogni feature gira ntô so git worktree sutta `~/.agentic-orchestrator/worktrees/` (li nstallazzioni legacy cuntìnuanu a usari `~/.agentic-workflow/worktrees/` finu a quannu faciti opt in). Chistu significa:
- Cchiù feature ponnu travagghiari supra lu stissu repo simultaniamenti
- Nun ci sunnu cunflitti di branch tra feature cuncurrenti
- La vostra working copy principali resta senza èssiri tuccata
- Li worktree si pulìscinu cu `c` doppu la completion

### Cchiù Repository

Ogni feature mira a unu o cchiù repository cu lu stissu lifecycle e la stissa state machine. Quannu na feature si stenni supra cchiù di nu repo, Agentic Orchestrator:
- Cria worktree nta ogni repo di destinazzioni
- Custruisci nu pianu d'esicuzzioni cu urdinamentu dî dependency tra repo
- Esegui implementation pi repo (n sequenza o n parallelu secunnu li dependency)
- Fa cross-reference dî PR tra repo autumaticamenti

Quannu na feature mira a nu sulu repo, lu pannellu Repo Progress pi repo, la cycle-selector modal e la tabella cross-reference PR si cumpàttanu — lu restu dû lifecycle resta idènticu.

### Knowledge Base

Prima di trasiri nta na feature, Agentic Orchestrator pò custruiri na knowledge base pi ogni repo — nu grafu strutturatu di ducumenti chi copri architecture, conventions, API surface, dependencies e mètudi di verification. La KB è misu n cache e attualizzata n modu incrementali (sulu quannu HEAD cancia), accussì li feature successivi ntô stissu repo partunu cchiù prestu.

### Gate di Validazzioni dû Pianu

Li piani sunnu rivisti di AI critic spicializzati prima chi l'implementation accumincia:

| Critic | Focus | Quannu è attivu |
|--------|-------|-------------|
| **Architecture** | Cuurinza dî pattern a liveddu roadmap, cunfini dî moduli, direzzioni dî dependency | Large/Moonshot, tutti li liveddi di risicu |
| **Structural** | Completezza dû phase plan, sizzioni nicissarii, forma dî task eseguìbbili | Large/Moonshot, tutti li liveddi di risicu |
| **Scope** | Cupertura dî requirement, tagghia dâ fasi, rilevamentu di over-engineering | Large/Moonshot, tutti li liveddi di risicu |
| **Security** | Auth, injection, prutizzioni dî dati calibrata supra lu cuntestu dû pruggettu | Large/Moonshot, autu risicu |
| **Performance** | Scalabilità, efficienza dî query, gestione dî risorsi | Large/Moonshot, autu risicu |
| **Testing** | Adeguatezza dâ cupertura, edge case, prutizzioni di regression | Phase plan Large/Moonshot, autu risicu |

Li critic gìranu n parallelu e prudùcinu verdict indipinnenti. Siddu un critic addumanna canciamenti, lu pianu veni rivistu e ri-validatu autumaticamenti. Medium sata li plan critic ma esegui sempri Final Review prima dû publish.

## Usu

### Dashboard TUI

Avviati cu `agentico`. La dashboard mustra tutti li feature urganizzati pi statu:

- **In Progress** — travagghiu attivu (researching, planning, implementing)
- **Published** — PR criata, aspittannu lu merge
- **Completed** — marcata comu fatta

Li feature chi addumannanu la vostra attinzioni (permessi pendenti, richiesti d'aiutu) mustranu n'indicaturi d'avvirtimentu.

### Criari na Feature

Premiti `n` dâ dashboard pi grapiri lu wizard:

1. **What** — Dati nu nomu e discriviti la feature. Supporta ncollari mmàggini (`Ctrl+V`) e attaching files (`@`).
2. **Where** — Scigghiti lu repo o li repo di destinazzioni. Sfugghiati carteddi novi o criati repo ô volu.
3. **Pipeline** — Scigghiti Medium, Large o Moonshot. Attivati o disattivati li checkpoint singuli (inquiry review, research review, design review, plan review, manual publish).
4. **Review** — Aggiustati liveddu di risicu, mudelli pi fasi ed exit criteria. Mannati pi partiri.

### Interagiri cu l'Agenti

**Watch** (`a`) — Grapi lu travagghiu live attivu n tempu riali. Lu stissu tastu addiventa **Answer**, **Approve** o **Review** quannu l'agenti havi bisognu d'input.

**Overview** (`o`) — Scancia lu pannellu drittu dâ dashboard di Live Preview â vista dittagghiata. Premiti `l` di Overview pi turnari a Live Preview; fora di Overview, `l` ancora grapi li logs.

**Stop watching** (`Esc/Ctrl+]`) — Turna â dashboard. L'agenti cuntinua a curriri.

### Azzioni Doppu l'Implementation

Quannu na feature arriva ô statu code-ready o published:

| Key | Action |
|-----|--------|
| `p` | Publish as PR (review dû diff → log dî commit → discrizzioni dâ PR → cunferma) |
| `t` | Tweak — fa nu canciamentu miratu senza rieseguiri tutta la pipeline |
| `Shift+F` | Refactor — applica nu prompt di refactoring all'implementation |
| `b` | Rebase on main |
| `g` | Talìa e risolvi li cummenti di PR review |
| `D` | Mark as done |

### Ask Me Anything

Premiti `/` unni vuliti pi grapiri la chat AI ncurpurata. È na sessioni Claude read-only chi pò spiegari comu funziona Agentic Orchestrator, diagnosticari prubblemi liggennu logs e artifacts dâ feature, circari ntô codebase e arrispùnniri ê dumanni — senza mudificari nuddu file.

### Keybindings

> Pi la rifirenza cumpleta, viditi [docs/keybindings.md](docs/keybindings.md).

## Cunfigurazzioni

La cunfigurazzioni sta nta `~/.agentic-orchestrator/config.yaml` (criatu autumaticamenti ô primu avviu). Siddu esisti già na cartedda legacy `~/.agentic-workflow/`, veni riusata ddà stissu accussì li nstallazzioni esistenti cuntìnuanu a funziunari senza copia manuali.

```yaml
defaults:
  models:
    research: "sonnet[200K]"     # Mudellu pi la fasi research
    planning: "opus[1M]"         # Mudellu pi la fasi planning
    implementation: "opus[1M]"   # Mudellu pi la fasi implementation
    review: "gpt-5.4[272K]"      # Mudellu pi la fasi review (Codex)
    utilities: "sonnet[200K]"    # Mudellu pi chat e task utility
    kb_build: "sonnet[200K]"     # Mudellu pi build dâ knowledge base
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
  inquireness: high          # Quantu spissu vennu mustrati li dumanni di planning
  pipeline: large            # Pipeline predefinita (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Scansionata pi git repo ô startup
```

### Suvrascritturi di mudelli

Ogni feature pò suvrascrìviri li mudelli predefiniti duranti la criazzioni attraversu lu wizard (passu 4). Li mudelli ponnu èssiri spicificati cu prifissi di provider espliciti (p'asempiu, `claude:opus[1M]`, `codex:gpt-5.4[272K]`) o comu alias nudi chi vennu instradati autumaticamenti ô provider chi currispunni megghiu.

### Flag d'avviu

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

### Attualizzari

```text
agentico update [--check|-n]
```

Eseguiti `agentico update` pi passari â cchiù ricenti release stabbili. Usati
`agentico update --check` (alias `-n`) pi fari vidiri la virsioni currenti e
la cchiù ricenti virsioni dispunìbbili senza nstallari nenti; nesci cu `0` e
stampa un missaggiu already-up-to-date quannu siti già supra la release cchiù nova.

## Sviluppu

```bash
# Custruzzioni
go build -o bin/agentico ./cmd/agentico

# O usati lu target make (scrivi ./bin/agentico)
make build

# Virìfica cutidiana
make test-fast

# Ginira li docs dî keybinding
go generate ./internal/tui/...
```

La virìfica è spartuta nta tier cu nomu, accussì li cuntrolli cutidiani ristanu
viloci mentri la cupertura allargata arresta dispunìbbili.

| Tier | Command | Tempu currenti | Scopu |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Cuntrollu cutidianu all-package in short-mode prima dû handoff. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Custruisci lu binariu e cuntrolla li flag CLI cchiù lu layout dî skill ncurpurati. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Cupertura dû lifecycle, dâ state-machine e dî protocol-violation. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Cumportamentu TUI e teatest cumpletu cu lu race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Cupertura d'integrazioni pi eventi TUI e feature-span appujata supra Observer. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Spazzata allargata all-package pi race/regression. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; nun misuratu | Scuperta live di skill/guideline contru CLI LLM riali. |

`go vet ./...` e `go build ./...` ristanu li cuntrolli statici e di build
nicissari. Lu tier marcatu **TUI observability** è lu gate opt-in esplicitu pi
la cupertura d'integrazioni TUI cchiù lenta appujata supra Observer. La spazzata
all-package cu race abbilitatu è lu tier **Race regression**, non lu cumannu
unit ordinariu. Viditi [AGENTS.md](AGENTS.md) e
[docs/testing-baseline.md](docs/testing-baseline.md) pi li dittagghi supra li
tempi, e viditi AGENTS.md pi lu mudellu isolated-run quannu vuliti eseguiri na
secunna istanza senza scontrarivi cu la prima.

## Cuntribbuiri

Li pull request sunnu binvinuti. Viditi [CONTRIBUTING.md](CONTRIBUTING.md) pi lu setup di sviluppu e li cunvinzioni di branch e commit.

Li cuntribbuti a stu pruggettu richiedinu d'accittari lu DoorDash Contributor License Agreement.
Viditi [CLA.md](CLA.md).

## Licenza

Agentic Orchestrator è distribbuitu sutta la [Apache License, Version 2.0](LICENSE.txt).

## Avvisi

Viditi [NOTICE.txt](NOTICE.txt) pi cumpunenti di terzi e attribbuzzioni.
