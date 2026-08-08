# Agentic Orchestrator

### Fai un oneshot della moonshot — poi rifallo dieci volte in parallelo.

Agentic Orchestrator è un orchestratore di workflow di sviluppo AI che trasforma qualsiasi ingegnere in un moltiplicatore di forza. Descrivi le tue feature, prendi le decisioni di alto livello, e l'AI si occupa del resto — ricerca, pianificazione, implementazione, code review, pull request — tutto in esecuzione concorrente da un singolo terminale.

> La CLI locale è `agentico`

<img width="3000" height="1800" alt="agentico-basic-flow-3000x1800" src="https://github.com/user-attachments/assets/b61ccb6e-3b0d-4b29-9b74-ade9a3917e82" />

## Perché Agentic Orchestrator?

La parte difficile della programmazione agentica non è chiedere a un modello di modificare dei file. La parte difficile è arrivare da una richiesta di feature vaga e di alto livello a una PR revisionabile senza perdere il contesto, saltare il lavoro di design, o lasciare che un piano scadente produca un diff enorme. Se lasciato senza gestione, è così che i team ottengono AI slop: codice plausibile prodotto più rapidamente del contesto, dei test e del processo di revisione necessari per renderlo affidabile. Agentic Orchestrator è costruito attorno a questo problema: trasforma un singolo prompt di feature in un workflow di ingegneria duraturo che raccoglie contesto, fa domande, progetta l'approccio, scompone il lavoro, lo implementa, lo verifica, lo revisiona e lo pubblica.

Questo è il vero valore dell'"oneshot": un ingegnere può descrivere una grande feature una sola volta, e poi supervisionare i checkpoint dove conta il giudizio, invece di seguire manualmente ogni prompt, sessione di terminale, worktree, esecuzione di test, passaggio di revisione e passaggio di PR.

- **Il contesto viene costruito, non sperato** — Le feature Large e Moonshot iniziano costruendo una knowledge base per repo, poi eseguono fasi di inquiry, ricerca e design prima della pianificazione. L'agente di implementazione legge artefatti strutturati invece di affidarsi a una singola cronologia di chat sovraccarica.
- **La complessità è suddivisa in fasi** — La pianificazione produce una roadmap, poi ogni fase della roadmap ottiene il proprio piano di fase dettagliato. Una fase tracer-bullet stabilisce il percorso; le successive fasi di riempimento TDD ritirano gli stub ed espandono la coverage.
- **I gate di qualità avvengono prima che il diff diventi costoso** — I validatori di piano revisionano architettura, ambito, struttura e, per il lavoro ad alto rischio, sicurezza, performance e testing. I loop di implementazione e di Final Review usano evidenze di verifica esplicite prima che la feature diventi pubblicabile.
- **L'attenzione umana è riservata alle decisioni** — Gate opzionali si fermano su revisione dell'inquiry, revisione della ricerca, revisione del design, revisione della roadmap, revisione del piano di fase, input utente e decisioni di pubblicazione. Tu approvi la direzione, richiedi un'iterazione o rispondi a domande mirate; l'orchestratore mantiene lo stato del workflow.
- **Il parallelismo è il moltiplicatore, non la premessa** — Poiché ogni feature ottiene worktree, branch, sessioni e artefatti isolati, puoi eseguire più workflow complessi contemporaneamente senza mescolare lo stato o bloccare il tuo checkout principale.
- **L'orchestrazione dei provider è esplicita** — Un provider è sufficiente per eseguire l'intero workflow; aggiungine altri per suddividere il lavoro. Claude, Codex e OpenCode sono co-equivalenti: il default di ogni fase è il miglior modello disponibile per quel ruolo tra tutti i provider rilevati, e i modelli possono essere sovrascritti per fase e scambiati a runtime. Usa `--providers` per limitare l'orchestratore alle CLI che hai effettivamente installato.

Il design segue i pattern descritti nell'articolo di Anthropic [Building Effective Agents](https://www.anthropic.com/engineering/building-effective-agents): prompt chaining, parallelizzazione, orchestrator-workers e loop evaluator-optimizer. Codifica inoltre il workflow [explore → plan → code](https://code.claude.com/docs/en/best-practices) di Claude Code e le linee guida di OpenAI su [orchestrazione e guardrail](https://openai.com/business/guides-and-resources/a-practical-guide-to-building-ai-agents/) degli agenti.

## Quick Start

Usa Homebrew se lo hai; altrimenti prendi il binario precompilato. Compila dal source solo se stai lavorando su agentico stesso.

**Homebrew** (raccomandato — macOS/Linux):

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
# assicurati che ~/.local/bin sia nel tuo PATH
```

**Dal source** — per contribuire ad agentico (Go 1.25+):

```bash
go install github.com/doordash-oss/agentic-orchestrator/cmd/agentico@latest
# oppure: git clone https://github.com/doordash-oss/agentic-orchestrator.git && cd agentic-orchestrator && make install
```

Poi esegui `agentico`. Aggiorna in qualsiasi momento con `agentico update` — usa il metodo giusto in base a come lo hai installato.

Al primo avvio, Agentic Orchestrator ti guida attraverso un flusso di benvenuto per selezionare le tue directory di workspace. Dopo, ti trovi sulla dashboard.

**Tre tasti da ricordare**: `n` (nuova feature), `?` (aiuto), `a` (osserva il lavoro attivo; rispondi, approva o revisiona quando richiesto). Tutto il resto è scopribile dall'overlay di aiuto.

## Prerequisiti

### Richiesti

| Tool | Scopo | Installazione |
|------|---------|-------|
| **`git`** | Operazioni di worktree, branch, commit e rebase | Preinstallato sulla maggior parte dei sistemi |
| **CLI `gh`** | Creazione di PR al momento del push e aggiornamento del body delle PR cross-repo durante il Publish | [Documentazione GitHub CLI](https://docs.github.com/en/github-cli/github-cli), poi `gh auth login` |

### CLI dei provider — installane almeno una

Agentic Orchestrator richiede **almeno una** CLI di provider AI.

| Tool | Ruolo | Installazione |
|------|------|---------|
| **Claude Code CLI >= 2.1.81** (`claude`) | Backend per KB, inquiry, ricerca, design, pianificazione, implementazione e chat | [Setup di Claude Code](https://code.claude.com/docs/en/getting-started) oppure `npm install -g @anthropic-ai/claude-code@latest` |
| **Codex CLI >= 0.116.0** (`codex`) | Backend per Final Review e per i modelli di review basati su Codex | [Setup di Codex CLI](https://developers.openai.com/codex/cli) oppure `npm i -g @openai/codex@latest` |
| **OpenCode CLI >= 1.17.9** (`opencode`) | Backend co-equivalente per ogni fase e per la chat; selezionato con `opencode:<backend/model>` (es. `opencode:anthropic/claude-sonnet-4-5`) | [opencode.ai](https://opencode.ai) oppure `curl -fsSL https://opencode.ai/install \| bash` |

OpenCode instrada un provider di backend configurato (Anthropic, OpenAI, Google, un modello Ollama locale, e così via) attraverso un'unica CLI. Autenticala con `opencode auth login`, e conferma che sia pronta con `opencode models`. Agentico esegue ogni sessione OpenCode su una configurazione gestita per-sessione e non modifica mai la tua configurazione globale di OpenCode. Attivala esplicitamente con `--providers opencode`, oppure lasciala aggiungere automaticamente quando la sua CLI è installata e autenticata.

### Opzionali

| Tool | Scopo | Installazione |
|------|---------|-------|
| **Go 1.25+** | Necessario solo per compilare `agentico` dal source — non richiesto quando si usa un [binario di release precompilato](#quick-start) | [go.dev](https://go.dev/dl/) |
| **Node.js 18+ e npm** | Necessario solo quando si installa Claude Code o Codex tramite npm | [nodejs.org](https://nodejs.org/) |

Dopo aver installato la/le tua/e CLI di provider, conferma che ciascuna sia autenticata — `claude auth status`, `codex login status`, e/o `opencode models` (elenca i modelli solo una volta configurato un provider di backend) — oltre a `gh auth status`, prima di lanciare `agentico`. Un provider la cui CLI è mancante, troppo vecchia, o non ancora autenticata viene filtrato all'avvio con un breve avviso, e l'orchestratore continua con qualunque provider sia pronto.

## Come Funziona

### Il Ciclo di Vita della Feature

Il ciclo di vita dipende dal profilo ed è guidato da checkpoint. Medium inizia dalla pianificazione. Large e Moonshot prima costruiscono il contesto, chiariscono l'intento ed esplorano le opzioni di design. Tutti i profili entrano poi nel loop della roadmap: creare una roadmap, pianificare una fase della roadmap alla volta, implementarla, eseguire il commit degli anchor di fase e continuare finché la fase finale non raggiunge la Final Review.

<img width="1051" height="570" alt="image" src="https://github.com/user-attachments/assets/00eb8559-0b0c-4000-a029-2210aa50f920" />

**Costruzione della Knowledge Base** — Costruisce o aggiorna una knowledge base per repo che copre architettura, convenzioni, superficie delle API, dipendenze e verifica. Le KB già aggiornate vengono riutilizzate e la fase viene saltata.

**Inquiry, Ricerca, Design** — Trasforma una richiesta di alto livello in risposte esplicite, risultati di ricerca e una direzione di design. Gli artefatti di Q&A vengono persistiti e passati in avanti in modo che le fasi successive non dipendano solo dalla memoria.

**Pianificazione della Roadmap e delle Fasi** — Crea la roadmap di livello superiore, poi un piano dettagliato per ogni fase della roadmap. Large e Moonshot eseguono i validatori di piano; Medium salta i critici di piano per un overhead minore.

**Implementazione** — Esegue un loop di implementazione di fase unificato attraverso l'insieme di repo delimitato dalla fase. Medium e Large si affidano alla Final Review; Moonshot mantiene inoltre la review per-iterazione durante l'implementazione.

**Final Review** — Viene eseguita una volta dopo l'ultima fase della roadmap, su ogni repo toccato che non è già stato pubblicato. La fase contiene il proprio loop di review/fix. Superare la Final Review porta la feature a `CodeReady`; esaurire il loop o violare il contratto di fase fa fallire la feature.

**Pubblicazione** — Se l'auto-publish è abilitato, Agentic Orchestrator esegue il commit, il rebase, il push, crea le PR e inietta automaticamente i link PR cross-repo. Se è abilitata la pubblicazione manuale, la TUI si ferma a `CodeReady` così puoi prima revisionare il diff e la descrizione della PR.

### Profili della Pipeline

Quando crei una feature, scegli una profondità di pipeline:

| Profilo | Fasi | Ideale per |
|---------|--------|----------|
| **Medium** | Piano di roadmap → loop di piano/implementazione per fase → Final Review → Publish | Modifiche piccole e ben comprese per cui conosci già l'approccio |
| **Large** | KB → Inquire → Research → Design → loop di roadmap → Final Review → Publish | La maggior parte delle feature complesse (default) |
| **Moonshot** | Stessa sequenza di fasi di Large, con effort elevato e review dell'implementazione per-iterazione | Modifiche ad alto rischio o altamente ambigue |

### Isolamento dei Worktree

Ogni feature viene eseguita nel proprio worktree git sotto `~/.agentic-orchestrator/worktrees/` (le installazioni legacy continuano a usare `~/.agentic-workflow/worktrees/` finché non si sceglie di aggiornare). Questo significa:
- Più feature possono lavorare sullo stesso repo contemporaneamente
- Nessun conflitto di branch tra feature concorrenti
- La tua copia di lavoro principale resta intatta
- I worktree vengono rimossi con `c` al termine

### Repository Multipli

Ogni feature ha come target uno o più repository con lo stesso ciclo di vita e la stessa macchina a stati. Quando una feature si estende su più di un repo, Agentic Orchestrator:
- Crea worktree in ogni repo di destinazione
- Costruisce un piano di esecuzione con ordinamento delle dipendenze tra i repo
- Esegue l'implementazione per-repo (in sequenza o in parallelo in base alle dipendenze)
- Referenzia automaticamente le PR tra i repo

Quando una feature ha come target un singolo repo, il pannello Repo Progress per-repo, la modale del selettore di ciclo e la tabella di cross-reference delle PR si comprimono — il resto del ciclo di vita è identico.

### Knowledge Base

Prima di immergersi in una feature, Agentic Orchestrator può costruire una knowledge base per repo — un grafo di documenti strutturato che copre architettura, convenzioni, superficie delle API, dipendenze e metodi di verifica. La KB è cachata e aggiornata in modo incrementale (solo quando HEAD cambia), così le feature successive nello stesso repo partono più rapidamente.

### Gate di Validazione del Piano

I piani sono revisionati da critici AI specializzati prima che inizi l'implementazione:

| Critico | Focus | Quando Attivo |
|--------|-------|-------------|
| **Architecture** | Coerenza dei pattern a livello di roadmap, confini dei moduli, direzione delle dipendenze | Large/Moonshot, tutti i livelli di rischio |
| **Structural** | Completezza del piano di fase, sezioni richieste, forma eseguibile dei task | Large/Moonshot, tutti i livelli di rischio |
| **Scope** | Coverage dei requisiti, dimensionamento della fase, rilevamento dell'over-engineering | Large/Moonshot, tutti i livelli di rischio |
| **Security** | Autenticazione, injection, protezione dei dati calibrata sul contesto del progetto | Large/Moonshot, rischio alto |
| **Performance** | Scalabilità, efficienza delle query, gestione delle risorse | Large/Moonshot, rischio alto |
| **Testing** | Adeguatezza della coverage, casi limite, protezione dalle regressioni | Piani di fase Large/Moonshot, rischio alto |

I critici vengono eseguiti in parallelo e producono verdetti indipendenti. Se un critico richiede modifiche, il piano viene rivisto e rivalidato automaticamente. Medium salta i critici di piano ma esegue comunque la Final Review prima della pubblicazione.

## Utilizzo

### Dashboard TUI

Avvia con `agentico`. La dashboard mostra tutte le feature organizzate per stato:

- **In Progress** — attivamente in lavorazione (in ricerca, pianificazione, implementazione)
- **Published** — PR creata, in attesa di merge
- **Completed** — segnata come completata

Le feature che richiedono la tua attenzione (permessi in sospeso, richieste di aiuto) mostrano un indicatore di avviso.

### Creare una Feature

Premi `n` dalla dashboard per aprire il wizard:

1. **What** — Nomina e descrivi la feature. Supporta l'incolla di immagini (`Ctrl+V`) e l'allegare file (`@`).
2. **Where** — Seleziona il/i repo di destinazione. Sfoglia per nuove directory o crea repo al volo.
3. **Pipeline** — Scegli Medium, Large o Moonshot e visualizza le opzioni di gate disponibili.
4. **Review** — Adatta il livello di rischio, i modelli per fase, i checkpoint (revisione dell'inquiry, revisione della ricerca, revisione del design, revisione della roadmap, revisione del piano di fase, pubblicazione manuale), i criteri di uscita. Invia per iniziare.

### Interagire con gli Agenti

**Watch** (`a`) — Apri il lavoro attivo in tempo reale. Lo stesso tasto diventa **Answer**, **Approve** o **Review** quando l'agente ha bisogno di input.

**Overview** (`o`) — Passa il pannello destro della dashboard da Live Preview alla panoramica dettagliata. Premi `l` da Overview per tornare a Live Preview; fuori da Overview, `l` apre comunque i log.

**Stop watching** (`Esc/Ctrl+]`) — Torna alla dashboard. L'agente continua a essere in esecuzione.

### Azioni Post-Implementazione

Una volta che una feature raggiunge lo stato code-ready o pubblicato:

| Tasto | Azione |
|-----|--------|
| `p` | Pubblica come PR (revisione del diff → log dei commit → descrizione della PR → conferma) |
| `b` | Rebase su main |
| `g` | Visualizza e risolvi i commenti di review della PR |
| `D` | Segna come completata |

### Ask Me Anything

Premi `/` ovunque per aprire la chat AI integrata. È una sessione di sola lettura — supportata da qualunque provider selezioni il tuo modello `utilities` (Claude, Codex o OpenCode) — che può spiegare come funziona Agentic Orchestrator, effettuare debug leggendo i log e gli artefatti delle feature, cercare nel codebase e rispondere a domande, senza modificare alcun file.

### Keybinding

> Per il riferimento completo, vedi [docs/keybindings.md](docs/keybindings.md).

## Configurazione

La configurazione si trova in `~/.agentic-orchestrator/config.yaml` (creata automaticamente al primo avvio). Se esiste già una directory legacy `~/.agentic-workflow/`, viene riutilizzata sul posto così le installazioni esistenti continuano a funzionare senza una copia manuale.

```yaml
defaults:
  models:
    inquiry: "sonnet[200K]"      # Modello per la fase Clarify/Inquire
    research: "sonnet[200K]"     # Modello per la fase di ricerca
    planning: "opus[1M]"         # Modello per la fase di pianificazione
    implementation: "opus[1M]"   # Modello per la fase di implementazione
    review: "gpt-5.4[272K]"      # Modello per la fase di review (Codex)
    utilities: "sonnet[200K]"    # Modello per chat e task di utilità
    kb_build: "sonnet[200K]"     # Modello per la costruzione della knowledge base
    automatic_review: ""         # Vuoto seleziona Automatic
  automatic_review_enabled: false
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
  inquireness: high          # Con quale frequenza vengono mostrate le domande di pianificazione
  pipeline: large            # Pipeline di default (medium, large, moonshot)

repos:
  my-service:
    path: /home/user/projects/my-service
    verification: "go test ./..."

workspace_roots:
  - /home/user/projects      # Analizzata per repo git all'avvio
```

### Override dei Modelli

Ogni feature può sovrascrivere i modelli di default durante la creazione tramite il wizard (passo 4). L'editor dei modelli mostra la fase Inquire come **Clarify**, separatamente da **Research**, così il chiarimento dei requisiti e la ricerca sul codebase possono usare modelli diversi. I modelli possono essere specificati con prefissi di provider espliciti (es. `claude:opus[1M]`, `codex:gpt-5.4[272K]`, `opencode:anthropic/claude-sonnet-4-5`) oppure come id semplici risolti rispetto al registro dei provider. Ci sono tre modi in cui una selezione raggiunge OpenCode, e sono distinti:

- Un **alias semplice** come `opus`, `sonnet` o `gpt-5.4` (senza slash) si risolve verso il proprio provider nativo (Claude o Codex) e **mai** verso OpenCode — OpenCode contribuisce solo id di backend in forma con slash.
- Il prefisso esplicito **`opencode:<provider>/<model>`** instrada sempre verso OpenCode, passando l'id di backend direttamente (funziona anche per un backend che OpenCode scopre ma che Agentico non pre-elenca).
- Un **id di backend in forma con slash semplice** come `anthropic/claude-sonnet-4-5` (senza prefisso) si risolve verso OpenCode quando corrisponde al catalogo di OpenCode. Questa è la forma che Agentico persiste per i default per-fase indipendenti dal provider quando OpenCode è l'unico provider pronto, quindi un modello OpenCode **può** essere un default senza alcun prefisso `opencode:` nella configurazione.

Usa `agentico --refresh-models` quando una CLI di provider mostra nuovi modelli ma Agentico mostra ancora un catalogo più vecchio. Il refresh esegue una discovery live per tutti i provider pronti, aggiorna la cache con chiave di versione in caso di successo, e ricade sulla cache precedente con un avviso se la discovery fallisce.

### Flag di Avvio

```text
agentico [flags]
agentico server [flags]

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

Esegui `agentico update` per aggiornare all'ultima release stabile. Usa
`agentico update --check` (alias `-n`) per riportare le versioni attuale e più
recente disponibile senza installare nulla; esce con `0` e stampa un
messaggio di già-aggiornato quando sei sull'ultima release.

## Sviluppo

```bash
# Build
go build -o bin/agentico ./cmd/agentico

# Oppure usa il target make (scrive ./bin/agentico)
make build

# Verifica quotidiana
make test-fast

# Genera la documentazione dei keybinding
go generate ./internal/tui/...
```

La verifica è suddivisa in livelli con nome così i controlli quotidiani restano
rapidi mentre la coverage estesa rimane disponibile.

| Livello | Comando | Tempo di esecuzione attuale | Scopo |
|------|---------|-------------------|---------|
| Fast suite | `make test-fast` | 23s, target <=30s | Controllo quotidiano in short-mode di tutti i package prima del passaggio di consegna. |
| E2E smoke shell | `bash test/e2e/smoke.sh` | 48.53s | Compila il binario e verifica i flag della CLI oltre al layout delle skill incorporate. |
| Isolated integration | `go test ./test/integration/... -count=1` | 323.06s | Coverage del ciclo di vita, della macchina a stati e delle violazioni di protocollo. |
| E2E Go (process-launch / API-driven) | `go test ./test/e2e/... -count=1 -race` | 41.51s | Comportamento completo della TUI e di process-launch con il race detector. |
| TUI observability | `go test -tags tui_observe ./internal/tui -run 'Observed|Emits' -count=1` | 15.14s | Coverage di integrazione basata su observer per eventi TUI e feature-span. |
| Race regression | `go test ./... -count=1 -race` | 158.82s | Sweep estesa di race/regressione su tutti i package. |
| Eval | `AGENTIC_EVAL=1 go test ./test/eval/... -count=1` | gated; not measured | Discovery live di skill/linee guida contro CLI LLM reali. |

`go vet ./...` e `go build ./...` restano controlli statici e di build richiesti.
Il livello **TUI observability** con tag è l'opt-in esplicito per la coverage
di integrazione TUI basata su observer, più lenta. Lo sweep con race abilitato
su tutti i package è il livello **Race regression**, non il comando ordinario
per unit test. Vedi
[AGENTS.md](AGENTS.md) e
[docs/testing-baseline.md](docs/testing-baseline.md) per i dettagli sui tempi, e
vedi AGENTS.md per il pattern di esecuzione isolata per eseguire una seconda
istanza senza entrare in collisione con la prima.

## Contribuire

Le pull request sono benvenute. Vedi [CONTRIBUTING.md](CONTRIBUTING.md) per il setup di sviluppo, le convenzioni di branch e commit.

I contributi a questo progetto richiedono l'accettazione del DoorDash Contributor License Agreement.
Vedi [CONTRIBUTOR_LICENSE_AGREEMENT.md](CLA.md).

## Licenza

Agentic Orchestrator è concesso in licenza secondo la [Apache License, Version 2.0](LICENSE.txt).

## Avvisi

Vedi [NOTICE.txt](NOTICE.txt) per i componenti di terze parti e le attribuzioni.
