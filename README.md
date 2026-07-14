# Agentic Orchestrator

### Un solo lancio per la missione lunare — poi ripetilo dieci volte in parallelo.

Agentic Orchestrator coordina flussi di sviluppo assistiti dall'IA e trasforma ogni ingegnere in un moltiplicatore di efficacia. Descrivi le funzionalità, prendi le decisioni di alto livello e l'IA gestirà il resto — ricerca, pianificazione, implementazione, revisione del codice e pull request — tutto in esecuzione concorrente da un unico terminale.

> Il CLI locale è `agentico`.

<img width="3000" height="1800" alt="agentico-basic-flow-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Perché Agentic Orchestrator?

La difficoltà del coding agentico non è chiedere a un modello di modificare dei file. È passare da una richiesta di funzionalità vaga e di alto livello a una PR verificabile senza perdere contesto, saltare la progettazione o lasciare che un piano mediocre produca un diff enorme. Senza controllo, i team ricevono codice plausibile ma scadente, generato più velocemente del contesto, dei test e della revisione necessari a renderlo affidabile. Agentic Orchestrator nasce per risolvere questo problema: trasforma un prompt di funzionalità in un flusso d'ingegneria persistente che raccoglie contesto, pone domande, progetta l'approccio, scompone il lavoro, lo implementa, lo verifica, lo revisiona e lo pubblica.

Questo è il vero valore di "oneshot": un ingegnere può descrivere una funzionalità ampia una sola volta e poi supervisionare i checkpoint in cui serve giudizio, invece di guidare manualmente ogni prompt, sessione di terminale, worktree, esecuzione dei test, revisione e passaggio della PR.

- **Il contesto si costruisce, non si spera** — Le funzionalità Large e Moonshot iniziano creando una knowledge base per repository, poi eseguono le fasi di analisi, ricerca e progettazione prima della pianificazione. L'agente di implementazione legge artefatti strutturati invece di affidarsi a una singola cronologia di chat sovraccarica.
- **La complessità è divisa in fasi** — La pianificazione produce una roadmap e ogni fase della roadmap riceve il proprio piano dettagliato. Una fase tracer-bullet stabilisce il percorso; le successive fasi TDD completano l'implementazione, eliminano gli stub e ampliano la copertura.
- **I gate di qualità arrivano prima che il diff diventi costoso** — I validatori esaminano architettura, ambito e struttura e, per il lavoro ad alto rischio, sicurezza, prestazioni e test. I cicli di implementazione e Final Review usano prove di verifica esplicite prima che la funzionalità sia pubblicabile.
- **L'attenzione umana è riservata alle decisioni** — I gate opzionali possono fermarsi per la revisione dell'analisi, della ricerca, del design, della roadmap, del piano di fase, per l'input dell'utente e per la pubblicazione. Approvi la direzione, chiedi un'iterazione o rispondi a domande mirate; l'orchestratore conserva lo stato del flusso.
- **Il parallelismo è il moltiplicatore** — Poiché ogni funzionalità riceve worktree, branch, sessioni e artefatti isolati, puoi eseguire contemporaneamente più flussi complessi senza mescolare lo stato o bloccare il checkout principale.
- **L'orchestrazione dei provider è esplicita** — Un provider basta per eseguire l'intero flusso; aggiungine altri per distribuire il lavoro. Claude, Codex e OpenCode sono sullo stesso piano: per ogni fase viene scelto il modello migliore disponibile per quel ruolo tra tutti i provider rilevati, con possibilità di sovrascriverlo per fase e cambiarlo a runtime. Usa `--providers` per limitare l'orchestratore ai CLI installati.

Il design segue i pattern descritti nell'articolo di Anthropic [Building Effective Agents](https://www.anthropic.com/engineering/building-effective-agents): concatenazione di prompt, parallelizzazione, orchestrator-worker e cicli evaluator-optimizer. Codifica inoltre il flusso [explore → plan → code](https://code.claude.com/docs/en/best-practices) di Claude Code e le indicazioni di OpenAI su [orchestration and guardrails](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/).

## Avvio rapido <a id="quick-start"></a>

Usa Homebrew se disponibile; altrimenti scarica il binario precompilato. Compila dai sorgenti solo se stai lavorando direttamente su agentico.

**Homebrew** (consigliato — macOS/Linux):

```bash
brew install doordash-oss/agentic-orchestrator/agentico
```

**Binario precompilato** — non servono Homebrew né Go (macOS/Linux, amd64/arm64):

```bash
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
TAG=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/doordash-oss/agentic-orchestrator/releases/latest | sed 's@.*/@@')
mkdir -p ~/.local/bin
curl -fsSL "https://github.com/doordash-oss/agentic-orchestrator/releases/download/${TAG}/agentic-orchestrator_${TAG#v}_${OS}_${ARCH}.tar.gz" | tar -xz -C ~/.local/bin agentico
# ensure ~/.local/bin is on your PATH
```

**Dai sorgenti** — per contribuire ad agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# or: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Poi esegui `agentico`. Puoi aggiornarlo in qualsiasi momento con `agentico update`: usa il metodo corretto in base all'installazione.

Al primo avvio, Agentic Orchestrator ti guida in un percorso di benvenuto per selezionare le directory di lavoro. Dopodiché visualizzi la dashboard.

**Tre tasti da ricordare**: `n` (nuova funzionalità), `?` (aiuto), `a` (osserva il lavoro attivo; rispondi, approva o revisiona quando richiesto). Tutto il resto è disponibile nella schermata di aiuto.

## Prerequisiti <a id="prerequisites"></a>

### Obbligatori

| Strumento | Scopo | Installazione |
|------|---------|---------|
| **`git`** | Operazioni su worktree, branch, commit e rebase | Preinstallato nella maggior parte dei sistemi |
| **`gh` CLI** | Creazione della PR al push e aggiornamento dei contenuti delle PR tra repository durante la pubblicazione | [Documentazione GitHub CLI](https://docs.github.com/en/github-cli/github-cli), poi `gh auth login` |

### CLI dei provider — installane almeno uno

Agentic Orchestrator richiede **almeno un** CLI di provider IA.

| Strumento | Ruolo | Installazione |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend per KB, analisi, ricerca, design, pianificazione, implementazione e chat | [Configurazione Claude Code](https://code.claude.com/docs/en/getting-started) oppure `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend per Final Review e modelli di revisione basati su Codex | [Configurazione Codex CLI](https://developers.openai.com/codex/cli) oppure `npm i -g @openai/codex@latest` |
| **OpenCode CLI >= 1.17.9** (`opencode`) | Backend allo stesso livello per ogni fase e per la chat; selezionato con `opencode:<backend/model>` (ad esempio `opencode:anthropic/claude-sonnet-4-5`) | [opencode.ai](https://opencode.ai) oppure `curl -fsSL https://opencode.ai/install \| bash` |

OpenCode instrada un provider backend configurato (Anthropic, OpenAI, Google, un modello Ollama locale e così via) attraverso un unico CLI. Autenticalo con `opencode auth login` e verifica che sia pronto con `opencode models`. Agentico esegue ogni sessione OpenCode con una configurazione gestita per sessione e non modifica mai la tua configurazione globale OpenCode. Attivalo esplicitamente con `--providers opencode`, oppure lascialo entrare automaticamente quando il CLI è installato e autenticato.
<!-- Compatibilità docs-contract: global OpenCode configuration. -->

### Facoltativi

| Strumento | Scopo | Installazione |
|------|---------|---------|
| **Go 1.25+** | Serve solo per compilare `agentico` dai sorgenti; non è necessario con un [binario di release precompilato](#quick-start) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ e npm** | Servono solo per installare Claude Code o Codex tramite npm | [nodejs.org](https://nodejs.org/) |

Dopo aver installato i CLI dei provider, verifica l'autenticazione di ciascuno — `claude auth status`, `codex login status` e/o `opencode models` (elenca i modelli solo dopo aver configurato un provider backend) — oltre a `gh auth status`, prima di avviare `agentico`. Un provider il cui CLI manca, è troppo vecchio o non è autenticato viene escluso all'avvio con un avviso sintetico; l'orchestratore continua con i provider pronti.

## Come funziona

### Ciclo di vita di una funzionalità

Il ciclo di vita dipende dal profilo ed è guidato dai checkpoint. Medium inizia dalla pianificazione. Large e Moonshot costruiscono prima il contesto, chiariscono l'intento ed esplorano le opzioni di design. Tutti i profili entrano poi nel ciclo della roadmap: creano una roadmap, pianificano una fase alla volta, la implementano, registrano gli anchor delle fasi e continuano fino a quando l'ultima fase raggiunge Final Review.

<img width="1051" height="570" alt="ciclo di vita di una funzionalità" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Creazione della knowledge base** — Costruisce o aggiorna una knowledge base per repository che copre architettura, convenzioni, superficie API, dipendenze e verifica. Le KB aggiornate vengono riutilizzate e la fase viene saltata.

**Analisi, ricerca e design** — Trasforma una richiesta di alto livello in risposte esplicite, risultati di ricerca e una direzione progettuale. Gli artefatti di domande e risposte vengono persistiti e passati alle fasi successive, che non dipendono così dalla sola memoria.

**Roadmap e pianificazione delle fasi** — Crea la roadmap di alto livello e poi un piano dettagliato per ciascuna fase. Large e Moonshot eseguono i validatori dei piani; Medium salta i critic dei piani per ridurre il carico.

**Implementazione** — Esegue un ciclo unificato di implementazione della fase sull'insieme di repository previsto dalla fase. Medium e Large si affidano a Final Review; Moonshot mantiene anche una revisione per iterazione durante l'implementazione.

**Final Review** — Viene eseguita una volta dopo l'ultima fase della roadmap, su ogni repository modificato che non sia già stato pubblicato. La fase contiene il proprio ciclo di revisione e correzione. Il superamento di Final Review porta la funzionalità a `CodeReady`; esaurire il ciclo o violare il contratto della fase fa fallire la funzionalità.

**Pubblicazione** — Se la pubblicazione automatica è attiva, Agentic Orchestrator crea commit, esegue rebase e push, apre le PR e inserisce automaticamente i link alle PR tra repository. Con la pubblicazione manuale, la TUI si ferma a `CodeReady` per permetterti di revisionare prima il diff e la descrizione della PR.

### Profili della pipeline

Quando crei una funzionalità, scegli la profondità della pipeline:

| Profilo | Fasi | Ideale per |
|---------|--------|----------|
| **Medium** | Piano della roadmap → ciclo piano/implementazione per fase → Final Review → pubblicazione | Modifiche piccole e comprese, quando l'approccio è già noto |
| **Large** | KB → analisi → ricerca → design → ciclo della roadmap → Final Review → pubblicazione | La maggior parte delle funzionalità complesse (predefinito) |
| **Moonshot** | Stessa sequenza di Large, con massimo impegno e revisione dell'implementazione a ogni iterazione | Modifiche ad alto rischio o molto ambigue |

### Isolamento dei worktree

Ogni funzionalità viene eseguita nel proprio worktree git sotto `~/.agentic-orchestrator/worktrees/` (le installazioni legacy continuano a usare `~/.agentic-workflow/worktrees/` finché non scegli di migrare). Questo significa:
- Più funzionalità possono lavorare contemporaneamente sullo stesso repository
- Nessun conflitto tra branch concorrenti
- La copia di lavoro principale resta intatta
- I worktree vengono puliti con `c` al termine

### Più repository

Ogni funzionalità può puntare a uno o più repository con lo stesso ciclo di vita e la stessa macchina a stati. Quando una funzionalità coinvolge più repository, Agentic Orchestrator:
- Crea worktree in ciascun repository di destinazione
- Costruisce un piano di esecuzione ordinato secondo le dipendenze tra repository
- Esegue l'implementazione per repository, in sequenza o in parallelo secondo le dipendenze
- Collega automaticamente le PR tra repository

Quando una funzionalità riguarda un solo repository, il pannello Repo Progress per repository, la finestra di selezione del ciclo e la tabella delle PR collegate vengono ridotti; il resto del ciclo di vita è identico.

### Knowledge base

Prima di entrare nel merito di una funzionalità, Agentic Orchestrator può costruire una knowledge base per repository: un grafo di documenti strutturati che copre architettura, convenzioni, superficie API, dipendenze e metodi di verifica. La KB viene messa in cache e aggiornata in modo incrementale, solo quando cambia HEAD, così le funzionalità successive nello stesso repository partono più rapidamente.

### Gate di validazione dei piani

Prima dell'inizio dell'implementazione, i piani vengono revisionati da critic IA specializzati:

| Critic | Ambito | Quando attivo |
|--------|-------|-------------|
| **Architettura** | Coerenza dei pattern a livello di roadmap, confini dei moduli, direzione delle dipendenze | Large/Moonshot, tutti i livelli di rischio |
| **Struttura** | Completezza del piano di fase, sezioni richieste, forma eseguibile dei task | Large/Moonshot, tutti i livelli di rischio |
| **Ambito** | Copertura dei requisiti, dimensionamento delle fasi, rilevamento dell'over-engineering | Large/Moonshot, tutti i livelli di rischio |
| **Sicurezza** | Autenticazione, injection e protezione dei dati calibrate al contesto del progetto | Large/Moonshot, rischio elevato |
| **Prestazioni** | Scalabilità, efficienza delle query, gestione delle risorse | Large/Moonshot, rischio elevato |
| **Testing** | Adeguatezza della copertura, casi limite, protezione dalle regressioni | Piani di fase Large/Moonshot, rischio elevato |

I critic lavorano in parallelo e producono valutazioni indipendenti. Se un critic richiede modifiche, il piano viene revisionato e rivalidato automaticamente. Medium salta i critic dei piani ma esegue comunque Final Review prima della pubblicazione.

## Utilizzo

### Dashboard TUI

Avvia con `agentico`. La dashboard mostra tutte le funzionalità organizzate per stato:

- **In Progress** — lavoro attivo (ricerca, pianificazione o implementazione)
- **Published** — PR creata, in attesa del merge
- **Completed** — contrassegnata come completata

Le funzionalità che richiedono attenzione (permessi in sospeso o richieste di aiuto) mostrano un indicatore di avviso.

### Creazione di una funzionalità

Premi `n` nella dashboard per aprire la procedura guidata:

1. **Cosa** — Dai un nome alla funzionalità e descrivila. Puoi incollare immagini (`Ctrl+V`) e allegare file (`@`).
<!-- Compatibilità vocabulary-test: attaching files. -->
2. **Dove** — Seleziona i repository di destinazione. Cerca nuove directory o crea repository al volo.
3. **Pipeline** — Scegli Medium, Large o Moonshot e visualizza le opzioni dei gate disponibili.
4. **Revisione** — Regola il livello di rischio, i modelli per fase, i checkpoint (revisione dell'analisi, della ricerca, del design, della roadmap, del piano di fase e pubblicazione manuale) e i criteri di uscita. Invia per iniziare.

### Interazione con gli agenti

**Osserva** (`a`) — Apri il lavoro attivo in tempo reale. Lo stesso tasto diventa **Rispondi**, **Approva** o **Revisiona** quando l'agente richiede un input.

**Panoramica** (`o`) — Passa il pannello destro della dashboard da Live Preview alla panoramica dettagliata. Premi `l` dalla panoramica per tornare a Live Preview; fuori dalla panoramica, `l` apre i log.

**Smetti di osservare** (`Esc/Ctrl+]`) — Torna alla dashboard. L'agente continua a lavorare.

### Azioni dopo l'implementazione

Quando una funzionalità raggiunge lo stato pronta per il codice o pubblicata:

| Tasto | Azione |
|-----|--------|
| `p` | Pubblica come PR (revisione del diff → log del commit → descrizione della PR → conferma) |
| `t` | Tweak — esegui una modifica mirata senza rilanciare l'intera pipeline |
| `Shift+F` | Refactor — applica un prompt di refactoring all'implementazione |
| `b` | Esegui rebase su main |
| `g` | Visualizza e risolvi i commenti di revisione della PR |
| `D` | Contrassegna come completata |

### Chiedimi qualsiasi cosa

Premi `/` ovunque per aprire la chat IA integrata. È una sessione in sola lettura — supportata dal provider scelto dal modello `utilities` (Claude, Codex o OpenCode) — che può spiegare come funziona Agentic Orchestrator, fare il debug leggendo log e artefatti, cercare nel codice e rispondere alle domande senza modificare file.

### Tasti rapidi

> Per il riferimento completo, consulta [docs/keybindings.md](docs/keybindings.md).

## Configurazione

La configurazione si trova in `~/.agentic-orchestrator/config.yaml` (creata automaticamente al primo avvio). Se esiste già una directory legacy `~/.agentic-workflow/`, viene riutilizzata in loco, così le installazioni esistenti continuano a funzionare senza copie manuali.

```yaml
defaults:
  models:
    inquiry: "sonnet[200K]"      # Model for Clarify/Inquire phase
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

### Override dei modelli

Ogni funzionalità può sovrascrivere i modelli predefiniti durante la creazione tramite la procedura guidata (passaggio 4). L'editor dei modelli mostra la fase Inquire come **Clarify**, separata da **Research**, così la chiarificazione dei requisiti e la ricerca nel codice possono usare modelli diversi. I modelli possono essere specificati con prefissi espliciti del provider (ad esempio `claude:opus[1M]`, `codex:gpt-5.4[272K]`, `opencode:anthropic/claude-sonnet-4-5`) oppure come ID nudi risolti dal registro dei provider. Esistono tre modalità distinte per raggiungere OpenCode:

- Un **plain alias** come `opus`, `sonnet` o `gpt-5.4` (senza slash) viene risolto dal provider nativo proprietario (Claude o Codex) e **mai** da OpenCode; OpenCode fornisce solo ID backend in formato slash.
- Il prefisso esplicito **`opencode:<provider>/<model>`** instrada sempre verso OpenCode e passa direttamente l'ID backend (funziona anche per un backend scoperto da OpenCode ma non pre-elencato da Agentico).
- Un **bare slash-form backend id** come `anthropic/claude-sonnet-4-5` (senza prefisso) viene risolto da OpenCode quando corrisponde al suo catalogo. È il formato che Agentico persiste per i **provider-neutral per-phase defaults** quando OpenCode è l'unico provider pronto, quindi un modello OpenCode **può** essere predefinito senza prefisso `opencode:` nella configurazione.
<!-- Compatibilità docs-contract: plain alias; bare slash-form; provider-neutral per-phase defaults. -->

Usa `agentico --refresh-models` quando il CLI di un provider mostra nuovi modelli ma Agentico continua a mostrare un catalogo precedente. L'aggiornamento esegue la discovery live per tutti i provider pronti, aggiorna la cache indicizzata per versione se ha successo e, in caso di errore, ricorre alla cache precedente con un avviso.

### Flag di avvio

```text
agentico [flags]

Flags:
  --config <path>                  Config file (default: ~/.agentic-orchestrator/config.yaml)
  --state-dir <path>               State directory (default: ~/.agentic-orchestrator/features)
  --dangerously-skip-permissions   Skip all permission prompts (use with caution)
  --providers <list>               Restrict to specific providers (claude,codex,opencode)
  --refresh-models                 Refresh provider model catalogs before opening the TUI
  --help, -h                       Show help
  --version, -v                    Show version
```

### Aggiornamento

```text
agentico update [--check|-n]
```

Esegui `agentico update` per passare all'ultima release stabile. Usa
`agentico update --check` (alias `-n`) per visualizzare la versione corrente e
l'ultima disponibile senza installare nulla; il comando termina con `0` e mostra
un messaggio che indica che è già aggiornato quando usi la release più recente.

## Sviluppo

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

La verifica è divisa in livelli denominati, così i controlli quotidiani restano
rapidi mentre la copertura estesa rimane disponibile.

| Livello | Comando | Durata attuale | Scopo |
|------|---------|-------------------|---------|
| Suite veloce | `make test-fast` | 23s, target <=30s | Controllo quotidiano in modalità short su tutti i package prima del passaggio di consegne. |
| Smoke shell E2E | `bash test/e2e/smoke.sh` | 48.53s | Compila il binario e controlla flag CLI e layout delle skill incorporate. |
| Integrazione isolata | `go test ./test/integration/... -count=1` | 323.06s | Copertura del ciclo di vita, della macchina a stati e delle violazioni di protocollo. |
| E2E Go (TUI / teatest) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Comportamento completo di TUI e teatest con race detector. |
| Osservabilità TUI | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Copertura d'integrazione degli eventi TUI e degli span della funzionalità basata su Observer. |
| Regressione race | `go test ./... -count=1 -race` | 158.82s | Scansione estesa di race e regressioni su tutti i package. |
| Valutazione | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | bloccato; non misurato | Discovery live di skill e linee guida tramite CLI LLM reali. |

`go vet ./...` e `go build ./...` restano controlli statici e di compilazione obbligatori.
Il livello con tag **TUI observability** è il gate esplicito per la copertura d'integrazione TUI più lenta basata su Observer. La scansione di tutti i package con race detector è il livello **Race regression**, non il normale comando per gli unit test. Consulta
[AGENTS.md](AGENTS.md) e
[docs/testing-baseline.md](docs/testing-baseline.md) per i dettagli sulle durate; consulta inoltre
AGENTS.md per il pattern di esecuzione isolata di una seconda istanza senza collisioni con la prima.

## Contribuire

Le pull request sono benvenute. Consulta [CONTRIBUTING.md](CONTRIBUTING.md) per la configurazione di sviluppo e le convenzioni per branch e commit.

Per contribuire al progetto è necessario accettare il DoorDash Contributor License Agreement.
Consulta [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## Licenza

Agentic Orchestrator è distribuito secondo [Apache License, Version 2.0](LICENSE.txt).

## Avvisi

Consulta [NOTICE.txt](NOTICE.txt) per i componenti di terze parti e le relative attribuzioni.
