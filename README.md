# Agentic Orchestrator

### Spara u moonshot cu nu sulu colpu — poi rifallu deci voti in parallilu.

Agentic Orchestrator è nu orchestraturi di workflow di sviluppu cu IA ca trasfurma ogni ingigneri nta nu multiplicaturi di forza. Deskrivi li tò feature, pigghia li decisioni di livellu àutu, e l'IA fa tuttu u restu — ricerca, pianificazzioni, implementazzioni, rivisioni di còdici, pull request — tuttu in parallilu da nu sulu terminali.

> U CLI lucali è `agentico`

<img width="3000" height="1800" alt="flussu-basìcu-agentico-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Pirchì propriu Agentic Orchestrator?

Lu puntu diffìcili dâ programmazzioni agentica nun è dumannari a nu mudellu di canciari file. Lu puntu diffìcili è arrivari di na dumanna di feature vaga e ad altu livellu a na pull request rivisabili senza perdiri cuntestu, saltari u travagghiu di disignu, o lassari ca nu pianu scarsu produci na diff gigantesca. Lassatu senza cuntrollu, chista è la via pi cui li squadri finisciunu cu AI slop: còdici ca pari fidatu ma è statu pruduciutu cchiù in fretta dâ quantità di cuntestu, testi e prucidura di rivisioni nicissaria pi fari lu fidatu. Agentic Orchestrator è custruitu apposta pi chistu prubbrema: pigghia na dumanna di feature e la trasforma in nu workflow d'ingignirìa dutàtuli ca racogghi cuntestu, fa dumanni, disigna l'approcciu, scumparti u travagghiu, lu implementa, lu verifica, lu rividi e lu pubblica.

Chistu è lu veru valuri di "oneshot": nu ingigneri po discrìviri na feature granni na vota sula, poi suprintènniri sulu li punti di cuntrollu unni lu giudiziu conta cchiù, invece di mannari a manu ogni prompt, sessioni di terminali, worktree, esecuzzioni di testi, passaggi di rivisioni e fasi di PR.

- **Lu cuntestu si custruisci, nun si spera** — Li feature Large e Moonshot accumincianu criannu na Knowledge Base pi ogni repo, poi passanu pi fasi di inquire, research e design prima dâ pianificazzioni. L'agenti di implementazzioni lèggi artefatti strutturati invece di stari appinnutu a na chat troppu carica.
- **La cumplessità è fasata** — La pianificazzioni pruduci na roadmap, poi ogni fasi dâ roadmap avi u so pianu detallatu. Na fasi tracer-bullet traccia la strata; li fasi cchiù tardivi di TDD riempinu li stub e allarganu la cupertura.
- **Li barrieri di qualità succedinu prima ca la diff diventa costusa** — Li validatori di pianu rivìdinu architittura, scopu, struttura e, pi u travagghiu ad altu risicu, sicurizza, prestazzioni e testi. Li cicli di implementazzioni e Final Review usanu prova di verifica esplicita prima ca la feature pò essiri pubblicata.
- **L'attenzioni umana è risirvata pi li decisioni** — Li punti di pausa opzziunali fermanu supra inquire, research, design, plan, user-input e publish. Tu appruvi la direzzioni, chiedi na iterazzioni, o rispunni a dumanni mirati; l'orchestraturi manteni u statu dû workflow.
- **Lu parallilismu è lu moltiplicaturi, nun lu puntu di partenza** — Siccomu ogni feature havi worktree, branch, sessioni e artefatti isolati, po fari curriri cchiù workflow cunchiusi nta lu stissu tempu senza mischjà statu o bluccari la tò main checkout.
- **L'orchestrazzioni dû provider è esplicita** — Un sulu provider è abbastanza pi fari curriri tuttu u workflow; aggiungine un secunnu pi spartiri lu travagghiu. Di difettu Claude gestisci cuntestu, pianificazzioni e implementazzioni mentri Codex gestisci rivisioni indipinnenti, ma li mudelli ponnu essiri rimpiazzati pi fasi e canciati a tempu di run. Usa `--providers` pi limitari l'orchestraturi sulu a li CLI ca hai davveru installati.

Lu disignu segui li patroni discritti nta l'articulu di Anthropic [Building Effective Agents](https://www.anthropic.com/engineering/building-effective-agents): prompt chaining, parallelization, orchestrator-workers, e evaluator-optimizer loops. Codifica puru u workflow di Claude Code [explore → plan → code](https://code.claude.com/docs/en/best-practices) e la guida di OpenAI supra l'orchestration e li guardrails di l'agenti [orchestration and guardrails](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/).

## Parti subitu

Usa Homebrew si lu hai; altrimenti pigghia lu binariu precompilatu. Custruisci di surgenti sulu si stai travagghiannu supra agentico stissu.

**Homebrew** (cunsigliatu — macOS/Linux):

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
# ensure ~/.local/bin is on your PATH
```

**Dâ surgenti** — pi cuntribbuiri a agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# or: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Poi esegui `agentico`. Aggiorna quannu ti servi cu `agentico update` — usa lu mètudu giustu pi comu l'hai installatu.

Quannu lanci la prima vota, Agentic Orchestrator ti guida nta nu flussu di benvenuta pi scegghiri li tò carteddi di travagghiu. Doppu di chissu, sì supra lu cruscottu.

**Tri tasti da arricurdari**: `n` (nova feature), `?` (ajutu), `a` (guarda u travagghiu attivu; rispunni, appruva, o rividi quannu ti veni dumandatu). Tuttu u restu si po scopriri dâ superfici d'ajutu.

## Chì ti servi

### Strettamenti necessarii

| Strumentu | Scopu | Installazzioni |
|------|---------|---------|
| **`git`** | Operazzioni di worktree, branch, commit e rebase | Già installatu supra assai sistemi |
| **`gh` CLI** | Creazzioni di PR e aggiornamenti cross-repo di u corpu dâ PR durante Publish | [GitHub CLI docs](https://docs.github.com/en/github-cli/github-cli), poi `gh auth login` |

### CLI di provider — nstalla almenu unu

Agentic Orchestrator avi bisognu di **almenu un** CLI di provider IA.

| Strumentu | Rolu | Installazzioni |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend predefinitu pi KB, inquire, research, design, planning, implementation e chat | [Claude Code setup](https://code.claude.com/docs/en/getting-started) o `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend predefinitu pi Final Review e mudelli di rivisioni basati supra Codex | [Codex CLI setup](https://developers.openai.com/codex/cli) o `npm i -g @openai/codex@latest` |

### Pi aviri cchiù cunfortu

| Strumentu | Scopu | Installazzioni |
|------|---------|---------|
| **Go 1.25+** | Serve sulu pi custruiri `agentico` di a surgenti — nun è nicissariu quannu usi na [release binaria precompilata](#avviu-rapidu) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ e npm** | Serve sulu quannu installi Claude Code o Codex attraversu npm | [nodejs.org](https://nodejs.org/) |

Doppu ca hai installatu li tò provider CLI, esegui `claude auth status` e/o `codex login status`, cchiù `gh auth status`, prima di lanciari `agentico`.

## Comu gira

### U ciclu di vita dâ feature

Lu ciclu dipenni dû prufilu e di li checkpoint. Medium accumencia dâ fase di pianificazzioni. Large e Moonshot custruiscinu prima u cuntestu, chiarìscinu l'intenzioni e esplòranu opzioni di disignu. Poi tutti li prufili entranu nta lu ciclu roadmap: creazzioni di na roadmap, pianificazzioni di na fasi di roadmap alla vota, implementazzioni, commit di ancore di fase, e avanti finu a quannu l'ultima fasi arriva a Final Review.

<img width="1051" height="570" alt="diagramma-dâ-feature" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Custruzzioni dâ Knowledge Base** — Custruisci o agghiorna na Knowledge Base pi ogni repo ca cupri architittura, cunvinzioni, API surface, dipindenzi e verificazzioni. Li KB freschi si riusanu e la fasi veni saltata.

**Inquire, Research, Design** — Trasforma na dumanna ad altu livellu nta risposti espliciti, risultati di ricerca e na direzzioni di disignu. Li artefatti di Q&A sunnu priservati e passati avanti accussì li fasi dopu nun dipènninu sulu dâ memoria.

**Roadmap e Pianificazzioni di Fasi** — Crea la roadmap di livellu cchiù àutu, poi na pianificazzioni dettagliata pi ogni fasi dâ roadmap. Large e Moonshot usanu li valutaturi di pianu; Medium salta li valutaturi di pianu pi aviri menu overhead.

**Implementazzioni** — Esegui un ciclu unificatu di implementazzioni supra lu set di repo di sta fasi. Medium e Large si fìdanu di Final Review; Moonshot manteni puru rivisioni pi ogni iterazzioni durante l'implementazzioni.

**Final Review** — Si fa na vota sola doppu l'ultima fasi dâ roadmap, supra ogni repo toccatu ca nun è già statu pubblicatu. La fasi havi u so ciclu di rivisioni e fix. Si Final Review passa, la feature và a `CodeReady`; si finisci u ciclu o si viola lu cuntrattu dâ fasi, la feature fallisci.

**Pubblicazzioni** — Si l'auto-publish è attivu, Agentic Orchestrator fa commit, rebase, push, crea PR e inserisci automaticamente li ligami cross-repo dâ PR. Si manual publish è attivu, lu TUI si ferma a `CodeReady` accussì puoi rivìdiri la diff e la discrizzioni dâ PR prima.

### I prufili dâ pipeline

Quannu crii na feature, scegghi nu livellu di pipeline:

| Prufilu | Fasi | Ideali pi |
|---------|--------|----------|
| **Medium** | Roadmap plan → ciclu plan/implement di ogni fasi → Final Review → Publish | Canciamenti picculi e ben caputi unni l'approcciu è già chiaru |
| **Large** | KB → Inquire → Research → Design → ciclu roadmap → Final Review → Publish | La maiò parti dî feature cumplicati (predefinitu) |
| **Moonshot** | Stissa sequenza di fasi di Large, ma cu max effort, valori predefiniti di plan-review, e rivisioni di implementazzioni pi ogni iterazzioni | Travagghi ad altu risicu o assai ambigui |

### Worktree senza mischiu

Ogni feature gira nta la so git worktree sottu `~/.agentic-orchestrator/worktrees/` (li installazzioni legacy cuntìnuanu a usari `~/.agentic-workflow/worktrees/` finu a quannu decidi di passari). Chistu voli diri:
- Più feature ponnu travagghiari supra u stissu repo in simultanea
- Nenti cunflittu di branch tra feature concurrenti
- La tò checkout principali resta micca tuccata
- Li worktree si pulìscinu cu `c` quannu finisci

### Cchiù repositori, stissu ritmu

Ogni feature mira unu o cchiù repositori cu la stissa lifecycle e state machine. Quannu na feature tocca cchiù di un repo, Agentic Orchestrator:
- Crea worktree nta ognunu dî repo mirati
- Custruisci un pianu d'esecuzzioni cu ordine di dipindenza tra repo
- Esegui l'implementazzioni pi repo (in sequenza o in parallilu secunnu li dipindenzi)
- Cross-reference automaticamenti li PR

Quannu na feature mira un sulu repo, u panel Repo Progress, lu modal cycle-selector e la cross-reference PR table si ridùcinu — u restu dâ lifecycle resta identicu.

### Na Knowledge Base pi ogni repo

Prima di affruntari na feature, Agentic Orchestrator po custruiri na Knowledge Base pi ogni repo — na grafa di ducumenti strutturati ca cupri architittura, cunvinzioni, API surface, dipindenzi e mètudi di verificazzioni. La KB veni cacheata e agghiornata incrementalmenti (sulu quannu HEAD cancia), accussì li feature successivi nta lu stissu repo accumincianu cchiù veloci.

### U cancellu di validazzioni dû pianu

Li piani sunnu rivisti di valutaturi IA specializzati prima ca l'implementazzioni accumencia:

| Criticu | Focu | Quannu è attivu |
|--------|------|----------------|
| **Architittura** | Cunsistenza dî patroni a livellu roadmap, cunfini di moduli, direzzioni dâ dipindenza | Large/Moonshot, tutti li livelli di risicu |
| **Strutturali** | Cumpiutezza dâ phase-plan, furmatu eseguibbili dî task richiesti | Large/Moonshot, tutti li livelli di risicu |
| **Scopu** | Cupertura dî richiesti, tagghiu di la fasi, rilevazzioni di over-engineering | Large/Moonshot, tutti li livelli di risicu |
| **Sicurizza** | Auth, injection, prutezzioni di dati calibrata supra lu cuntestu dû prughjettu | Large/Moonshot, risicu àutu |
| **Prestazzioni** | Scalabilità, efficienza di query, gestioni di risorsi | Large/Moonshot, risicu àutu |
| **Testi** | Adeguatezza dâ cupertura, casi estremi, prutezzioni di regressioni | Large/Moonshot phase plans, risicu àutu |

Li valutaturi currunu in parallilu e prudùcinu verditti indipinnenti. Si ogni valutaturi dumanna canciamenti, lu pianu veni rivistu e validatu di novu in automaticu. Medium salta li valutaturi di pianu ma manteni Final Review prima dâ pubblicazzioni.

## Comu si usa

### U cruscottu TUI

Lancia cu `agentico`. U cruscottu mostra tutti li feature urganizzati pi statu:

- **In Progress** — travagghiu attivamenti in corsu (researching, planning, implementation)
- **Published** — PR criata, aspetta merge
- **Completed** — marcatu comu finitu

Li feature ca avìssiru bisognu di la tò attinzioni (permessi pendenti, richiesti d'aiutu) mustranu n'avvisu.

### Metti na feature in marcia

Pressa `n` dû cruscottu pi apiri lu wizard:

1. **Cosa** — Nomi e discrizzioni dâ feature. Supporta incollari immagini (`Ctrl+V`) e `attaching files` (`@`).
2. **Unni** — Scegghi lu/i repo destinazzioni. Sfogghia pi carteddi novi o crea repo sul locu.
3. **Pipeline** — Scegghi Medium, Large, o Moonshot. Attiva o disattiva checkpoint individuali (inquiry review, research review, design review, plan review, manual publish).
4. **Rivisioni** — Regula risicu, mudelli pi fasi, exit criteria. Manna pi accumincia.

### Parlari cu l'agenti

**Guarda** (`a`) — Apre u travagliu attivu live. La stissa tasta diventa **Risponni**, **Appruva**, o **Rividi** quannu l'agenti ti dumanna.

**Panorama** (`o`) — Cambia lu panel dirittu di lu cruscottu di Live Preview a la panoramica dettagliata. Pressa `l` di Panorama pi turnari a Live Preview; fora di Panorama, `l` apre ancora li registri.

**Ferma di guardari** (`Esc/Ctrl+]`) — Torna a lu cruscottu. L'agenti cuntìnuanu a travagghiari.

### Cosa fari doppu l'implementazzioni

Quannu na feature arriva a code-ready o published:

| Tasti | Azzioni |
|-----|--------|
| `p` | Pubblica comu PR (rivisioni dâ diff → commit log → discrizzioni dâ PR → cunferma) |
| `t` | Tweak — fa na mudìfica mirata senza rifari tuttu u pipeline |
| `Shift+F` | Refactor — applica nu prompt di rifatturazzioni all'implementazzioni |
| `b` | Rebase supra main |
| `g` | Vidi e risolvi li cummenti di rivisioni dâ PR |
| `D` | Marca comu finitu |

### Dumannami cchiù cose

Pressa `/` nta ogni puntu pi apiri la chat IA interna. È na sessione Claude sulu in lettura ca po spiegari comu funziuna Agentic Orchestrator, debuggari prublemi leggennu li registri e artefatti di feature, circari nta la base di còdici, e rispùnniri a dumanni — senza canciari nuddu file.

### Tasti Scurciaturi

> Pi la referenza cumplèta, vidi [docs/keybindings.md](docs/keybindings.md).

## Cunnfigurazzioni

La cunnfigurazzioni sta ntô `~/.agentic-orchestrator/config.yaml` (criatu automaticamenti a la prima accensioni). Si esisti già na cartella legacy `~/.agentic-workflow/`, veni riusata invece di fari na copia manuale.

```yaml
defaults:
  models:
    research: "sonnet[200K]"     # Model for research phase
    planning: "opus[1M]"         # Model for planning phase
    implementation: "opus[1M]"   # Model for implementation phase
    review: "gpt-5.4[272K]"      # Model for review phase (Codex)
    utilities: "sonnet[200K]"    # Model for chat and utility tasks
    kb_build: "sonnet[200K]"     # Model for knowledge base builds
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

### Mudelli a modu tò

Ogni feature po rimpiazzari li mudelli predefiniti durante la creazzioni via lu wizard (passu 4). Li mudelli ponnu essiri specificati cu prefissi espliciti di provider (pi esempiu, `claude:opus[1M]`, `codex:gpt-5.4[272K]`) o comu alias senza prefissu ca sunnu indirizzati automaticamenti versu lu provider cchiù adattu.

### Flags di partenza

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

### Aggiornamenti

```text
agentico update [--check|-n]
```

Esegui `agentico update` pi agghiurnari a l'ùrtima release stabile. Usa
`agentico update --check` (alias `-n`) pi diri la versioni attuali e la cchiù
nova dispunibbili senza installari nenti; torna `0` e stampa nu missaggiu già
aggiornatu quannu sì supra l'ùrtima release.

## Sviluppu

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

La verificazzioni è spartuta nta tier nominati accussì li cuntrolli cutidiani ristanu veloci mentri la cupertura estisa resta dispunibbili.

| Tier | Cumannu | Tempu wall attuali | Scopu |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Verifica cutidiana short-mode pi tutti li package prima dâ consegna. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Custruisci lu binariu e controlla li flags di CLI cchiù la disposizzioni embeddata dî skill. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Cupertura di lifecycle, state-machine e protocol-violation. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Cumportamentu cumpletu di TUI e teatest cu lu race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Cupertura d'integrazioni di eventi TUI e feature-span cun Observer. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Sweep all-package estisu pi race/regression. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Scuperta live di skill/guideline supra CLI LLM reali. |

`go vet ./...` e `go build ./...` ristanu cuntrolli statici e di build richiesti.
Lu tier marcatu **TUI observability** è lu gate esplicitu opt-in pi la cupertura cchiù lenta d'integrazione TUI basata supra observer. Lu sweep all-package cu race attivu è lu tier **Race regression**, nun lu cumannu unitariu d'ogni jornu. Vidi [AGENTS.md](AGENTS.md) e
[docs/testing-baseline.md](docs/testing-baseline.md) pi li dittagghi di tempu, e
vidi AGENTS.md pi lu patruni di run isolatu pi eseguiri na secunna istanza senza
collisioni cu la prima.

## Cuntribbuisce

Li pull request sunnu benvenuti. Vidi [CONTRIBUTING.md](CONTRIBUTING.md) pi la cunnfigurazzioni di sviluppu e li cunvenzioni di branch e commit.

Li cuntribbuti a stu prughjettu richèdinu d'accittari lu DoorDash Contributor License Agreement.
Vidi [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## Licenza

Agentic Orchestrator è licinziatu sutta la [Apache License, Version 2.0](LICENSE.txt).

## Noti

Vidi [NOTICE.txt](NOTICE.txt) pi li cumpunenti di terzi e li attribuzzioni.
