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

## Comm' Funziona

### 'O Ciclo 'e Vita d''a Feature

'O ciclo 'e vita dipende d''o profile ed è guidato d''e checkpoint. Medium parte d''a pianificazione. Large e Moonshot prima costruiscono 'o contesto, chiariscono l'intento e esplorano 'e opzione 'e design. Tutti 'e profile entrano po' d''o loop d''o Roadmap: si crea 'nu Roadmap, se pianifica 'na fase â volta, s'implementa, se registrano 'e anchor 'e fase, e si va avanti fin'a quann' l'ultima fase arriva â Final Review.

<img width="1051" height="570" alt="image" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Knowledge Base Build** — Costruisce o aggiorna 'na knowledge base per repo ca copre architettura, convenzione, superficie API, dipendenze e verifica. 'E KB fresche vengono riutilizzate e 'a fase viene saltata.

**Inquire, Research, Design** — Trasforma 'na richiesta 'e alto livello 'n risposte esplicite, risultati 'e ricerca e 'na direzione 'e design. L'artefatti Q&A vengono persistiti e passati avanti, accossì 'e fasi successive nun dipendono sulo d''a memoria.

**Roadmap and Phase Planning** — Crea 'o Roadmap 'e alto livello, e po' 'nu piano dettagliato pe ogni fase d''o Roadmap. Large e Moonshot eseguono 'e validatori 'e piano; Medium salta 'e plan critics pe ridurre 'o sovraccarico.

**Implementation** — Esegue 'nu loop 'e implementazione unificato pe ogni fase su tutto 'o conjunto 'e repo appartenenti â fase. Medium e Large si affidano â Final Review; Moonshot mantiene pure 'na revisione per iterazione durante l'implementazione.

**Final Review** — Gira 'na vota doppo l'ultima fase d''o Roadmap, su ogni repo toccato ca nun è stato ancora pubblicato. 'A fase tene 'o proprio loop review/fix. Superà 'a Final Review porta 'a feature a `CodeReady`; esaurire 'o loop o violare 'o contratto 'e fase fa fallire 'a feature.

**Publishing** — Si 'o publish automatico è attivato, Agentic Orchestrator esegue commit, rebase, push, crea 'e PR e inietta 'e link cross-repo d''e PR automaticamente. Si 'o publish manuale è attivato, 'o TUI si ferma a `CodeReady` accossì puoi rivedere 'a diff e 'a descrizione d''a PR prima.

### Profile 'e Pipeline

Quann' create 'na feature, sceglie 'a profondità d''o pipeline:

| Profile | Phases | Best for |
|---------|--------|----------|
| **Medium** | Roadmap plan → per-phase plan/implement loop → Final Review → Publish | Cambiamenti piccoli e ben compresi, d''u quale conosci già l'approccio |
| **Large** | KB → Inquire → Research → Design → roadmap loop → Final Review → Publish | 'A maggior parte d''e feature complesse (predefinito) |
| **Moonshot** | Stessa sequenza 'e fasi d''o Large, c''o massimo sfurzo, valori predefiniti 'e plan-review, e revisione d''a implementazione a ogni iterazione | Cambiamenti ad alto rischio o molto ambigui |

### Isolamento d''o Worktree

Ogni feature gira d''o proprio git worktree sott'a `~/.agentic-orchestrator/worktrees/` (l'installazioni legacy continuano a usare `~/.agentic-workflow/worktrees/` fin'a quann' nun optate). Chisto significa:
- Cchiù feature possono lavorare 'ncopp'ô stesso repo contemporaneamente
- Nessun conflitto 'e branch tra feature concorrenti
- 'A tua copia principale d''o lavoro rimane intatta
- 'E worktrees vengono puliti c''o `c` doppo 'o completamento

### Cchiù Repository

Ogni feature punta a uno o cchiù repository c''o stesso ciclo 'e vita e state machine. Quann' 'na feature si estende su cchiù 'e nu repo, Agentic Orchestrator:
- Crea worktrees d''o ogni repo destinatario
- Costruisce 'nu piano d'esecuzione c''o ordinamento d''e dipendenze tra repo
- Esegue l'implementazione per-repo (sequenzialmente o in parallelo basandosi sulle dipendenze)
- Fa 'o cross-referencing d''e PR tra repo automaticamente

Quann' 'na feature punta a 'nu solo repo, 'o pannello Repo Progress per-repo, 'o modal cycle-selector e 'a tabella cross-reference d''e PR si contraggono — 'o rimanente d''o ciclo 'e vita è identico.

### Knowledge Base

Prima 'e buttarsi 'ncopp'a 'na feature, Agentic Orchestrator po' costruire 'na knowledge base per repo — 'nu grafo 'e documenti strutturato ca copre architettura, convenzione, superficie API, dipendenze e metodi 'e verifica. 'A KB viene memorizzata in cache e aggiornata in modo incrementale (sulo quann' HEAD cambia), accossì 'e feature successive d''o stesso repo partono cchiù veloci.

### Plan Validation Gate

'E piani vengono revisionati d''e critics AI specializzati prima ca l'implementazione inizi:

| Critic | Focus | When Active |
|--------|-------|-------------|
| **Architecture** | Consistenza 'e pattern a livello 'e Roadmap, confini d''e moduli, direzione d''e dipendenze | Large/Moonshot, tutti i livelli 'e rischio |
| **Structural** | Completezza d''o piano 'e fase, sezioni richieste, struttura 'e task eseguibili | Large/Moonshot, tutti i livelli 'e rischio |
| **Scope** | Copertura d''e requisiti, dimensione d''e fasi, rilevamento 'e over-engineering | Large/Moonshot, tutti i livelli 'e rischio |
| **Security** | Auth, injection, protezione d''e dati calibrata al contesto d''o progetto | Large/Moonshot, rischio alto |
| **Performance** | Scalabilità, efficienza d''e query, gestione d''e risorse | Large/Moonshot, rischio alto |
| **Testing** | Adeguatezza d''a copertura, casi limite, protezione d''e regressioni | Piani 'e fase Large/Moonshot, rischio alto |

'E Critics girano in parallelo e producono verdetti indipendenti. Si qualsiasi critic richiede modifiche, 'o piano viene revisionato e ri-validato automaticamente. Medium salta 'e plan critics ma esegue ugualmente 'a Final Review prima d''o publish.

## Utilizzo

### Dashboard TUI

Avviate c''o `agentico`. 'O dashboard mostra tutte 'e feature organizzate per status:

- **In Progress** — attivamente in lavorazione (ricerca, pianificazione, implementazione)
- **Published** — PR creata, in attesa 'e merge
- **Completed** — segnata comm' completata

'E feature ca necessitano d''a vostra attenzione (permessi pendenti, richieste 'e aiuto) mostrano 'nu indicatore 'e avvertimento.

### Creare 'na Feature

Premete `n` d''o dashboard pe aprire 'o wizard:

1. **Cosa** — Nome e descrivi 'a feature. Supporta l'incollaggio d'immagini (`Ctrl+V`) e attaching files (`@`).
2. **Dove** — Seleziona 'o/i repo destinatari. Sfoglia pe nuove directory o crea repo al volo.
3. **Pipeline** — Scegli Medium, Large o Moonshot. Abilita/disabilita 'e checkpoint singoli (revisione inquiry, revisione ricerca, revisione design, revisione piano, publish manuale).
4. **Revisione** — Regola 'o livello 'e rischio, 'e modelli per fase, 'e exit criteria. Invia pe iniziare.

### Interagire c''e Agenti

**Watch** (`a`) — Apre 'o lavoro live attivo in tempo reale. 'A stessa chiave diventa **Answer**, **Approve** o **Review** quann' l'agente ha bisogno 'e input.

**Overview** (`o`) — Cambia 'o pannello destro d''o dashboard d''o Live Preview â panoramica dettagliata. Premi `l` d''o Overview pe tornare â Live Preview; fuori d''o Overview, `l` apre ugualmente 'e log.

**Stop watching** (`Esc/Ctrl+]`) — Torna â dashboard. L'agente continua a girare.

### Azioni Post-Implementazione

Doppo ca 'na feature arriva a code-ready o a stato pubblicato:

| Key | Action |
|-----|--------|
| `p` | Pubblica comm' PR (revisione diff → log commit → descrizione PR → conferma) |
| `t` | Tweak — fa 'nu cambiamento mirato senza rigirare l'intero pipeline |
| `Shift+F` | Refactor — applica 'nu prompt 'e refactoring all'implementazione |
| `b` | Rebase su main |
| `g` | Visualizza e risolve 'e comment 'e revisione d''a PR |
| `D` | Segna comm' completata |

### Chiede Qualsiasi Cosa

Premi `/` d'ovunque pe aprire 'a chat AI integrata. È 'na sessione Claude in sola lettura ca po' spiegare comm' funziona Agentic Orchestrator, debuggare problemi leggendo 'e log e l'artefatti d''a feature, cercare d''o codebase e rispondere a domande — senza modificare nessun file.

### Keybindings

> Pe 'a referenza completa, vedi [docs/keybindings.md](docs/keybindings.md).

## Configurazione

'A configurazione si trova a `~/.agentic-orchestrator/config.yaml` (creata automaticamente al primo avvio). Si esiste già 'na directory legacy `~/.agentic-workflow/`, viene riutilizzata così comm'è, accossì 'e installazioni esistenti continuano a funzionare senza copia manuale.

```yaml
defaults:
  models:
    research: "opus[1m]"     # Modello pe 'a fase 'e ricerca
    planning: "opus[1m]"     # Modello pe 'a fase 'e pianificazione
    implementation: "opus[1m]" # Modello pe 'a fase 'e implementazione
    review: gpt-5.4          # Modello pe 'a fase 'e revisione (Codex)
    utilities: sonnet        # Modello pe 'e task 'e chat e utility
    kb_build: "opus[1m]"     # Modello pe 'a costruzione d''a knowledge base
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
  inquireness: high          # Quanto spesso vengono sollevate 'e domande 'e pianificazione
  pipeline: large            # Pipeline predefinito (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Scansionato pe repo git all'avvio
```

### Override d''e Modelli

Ogni feature po' sovrascrivere 'e modelli predefiniti durante 'a creazione tramite 'o wizard (passo 4). 'E modelli possono essere specificati c''e prefissi 'e provider espliciti (ad es. `claude:opus`, `codex:gpt-5.4`) o comm' nomi semplici ca vengono automaticamente instradati â provider cchiù adatta.

### Flag 'e Avvio

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

### Aggiornamento

```text
agentico update [--check|-n]
```

Esegui `agentico update` pe aggiornare all'ultima release stabile. Usa
`agentico update --check` (alias `-n`) pe riportare 'a versione attuale e
l'ultima disponibile senza installare niente; esce con `0` e stampa 'nu
messaggio 'e già aggiornato quann' sei sull'ultima release.

## Sviluppo

```bash
# Compilazione
go build -o bin/agentico ./cmd/agentico

# O usa 'o make target (scrive ./bin/agentico)
make build

# Verifica quotidiana
make test-fast

# Genera 'a documentazione d''e keybinding
go generate ./internal/tui/...
```

'A verifica è suddivisa in livelli nominati accossì 'e verifiche quotidiane
restano veloci mentre 'a copertura estesa rimane disponibile.

| Tier | Command | Current wall time | Purpose |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Verifica quotidiana 'e tutti i pacchetti in modalità breve prima d''o handoff. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Costruisce 'o binario e verifica 'e flag CLI cchiù 'o layout d''e skill incorporati. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Copertura d''o ciclo 'e vita, state-machine e violazioni 'e protocollo. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Comportamento completo d''o TUI e teatest c''o race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Copertura d'integrazione d''e eventi TUI e feature-span supportata d''o observer. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Sweep esteso d''a race/regressione su tutti i pacchetti. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Scoperta live d''e skill/linee guida su LLM CLI reali. |

`go vet ./...` e `go build ./...` restano 'e verifiche statiche e 'e build
obbligatorie. 'O livello **TUI observability** taggato è 'o gate opt-in
esplicito pe 'a copertura d'integrazione TUI supportata d''o observer cchiù
lenta. 'O sweep su tutti i pacchetti con race abilitato è 'o livello
**Race regression**, nun 'o comando unit ordinario. Vedi
[AGENTS.md](AGENTS.md) e
[docs/testing-baseline.md](docs/testing-baseline.md) pe 'e dettagli d''e
tempi, e vedi AGENTS.md pe 'o pattern 'e esecuzione isolata pe girare 'na
seconda istanza senza collidere c''a prima.

## Contribuire

'E pull request sono benvenute. Vedi [CONTRIBUTING.md](CONTRIBUTING.md) pe
'a configurazione d''o sviluppo e 'e convezione 'e branch e commit.

'E contribuzioni a chisto progetto richiedono l'accordo c''o DoorDash Contributor License Agreement.
Vedi [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## Licenza

Agentic Orchestrator è rilasciato sotto 'a [Apache License, Version 2.0](LICENSE.txt).

## Avvisi

Vedi [NOTICE.txt](NOTICE.txt) pe 'e componenti di terze parti e 'e attribuzioni.
