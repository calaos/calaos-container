# T7 — Vérification end-to-end

- **Statut** : reviewed
- **Modèle** : opus — **Effort** : high — **Skills** : `smoke-test`, `verify`
- **Dépend de** : T1–T6

## Objectif

Valider la stack complète en conditions réelles dans le devcontainer (privilégié, systemd et apt fonctionnels) : daemon + WebSocket + upgrade apt réel avec progression + CLI.

## Checklist de vérification

1. **Build & lint** : `make` complet ; `make test` ; `CGO_LDFLAGS="-L./bin -lcalaos-apt" golangci-lint run` propre. Surveiller le link (double compilation cgo/meson : pas de symbole dupliqué).
2. **Setup** : `mkdir -p /run/calaos && echo test-token > /run/calaos/calaos-ct.token` ; daemon : `LD_LIBRARY_PATH=./bin ./bin/calaos-container -c ./calaos-container.toml` (ou via la skill `smoke-test`).
3. **Endpoint /api/events** :
   - `curl -H "Authorization: Bearer test-token" http://127.0.0.1:8000/api/events` → **426** ;
   - sans token → **401** ;
   - `websocat -H="Authorization: Bearer test-token" ws://127.0.0.1:8000/api/events` et variante `?token=test-token` → snapshot `{"type":"upgrade_progress","data":{"status":"idle",...}}` puis pings (si websocat absent : mini client Go gorilla dans le scratchpad).
4. **Upgrade réel avec progression** : downgrader un petit paquet (`apt-get install --allow-downgrades <pkg>=<version-antérieure>` ou .deb N-1 via `apt-get download`/snapshot.debian.org, `dpkg -i`) ; `POST /api/update/check` → le paquet apparaît dans `/api/update/available` ; `POST /api/update/upgrade?package=<pkg>` puis scénario `upgrade-all` → observer sur websocat la séquence `pending → download → unpack → configure → done`, % global **monotone** 0→100, `packages[]` cohérent, puis snapshot `GET /api/update/status` cohérent pendant l'opération.
5. **Cas d'erreur** : source apt invalide ou réseau coupé pendant le fetch → event `status:"error"` avec message `_error` ; un second upgrade derrière fonctionne (locks et pile d'erreurs purgés).
6. **CLI** : `./bin/calaos-os upgrade` → barre globale + étapes par paquet, ligne ✔ par paquet, exit 0 ; `upgrade <pkg>` idem ; contre un daemon compilé depuis `main` (sans /api/events) → message « daemon trop ancien » + exit 1.
7. **Concurrence** : `GET /api/update/check` pendant un upgrade → « upgrade already in progress » (sémaphore inchangé).
8. **Shutdown** : arrêt du daemon avec un client WS connecté → close frame propre, pas de blocage ; `NewVersions` rafraîchi après upgrade (le paquet upgradé disparaît de `/api/update/available`).
9. **Journald/logs** : sortie dpkg visible (Use-Pty=false), aucun prompt conffile (confold forcé).

## Livrable

Rapport de vérification ajouté en bas de ce fichier : chaque point ✔/✘ avec les sorties observées (extraits), et la liste des anomalies à corriger (renvoyées aux tasks concernées avant T8).

## Critères d'acceptation

- Tous les points de la checklist ✔ (ou anomalies corrigées puis re-vérifiées).

## Review

Vérification e2e réalisée dans le devcontainer (privilégié, systemd + apt réels, Debian bookworm).
Scénario réel : downgrade de `libtasn1-6` (4.19.0-2+deb12u1 → 4.19.0-2, .deb récupéré sur
snapshot.debian.org) puis upgrade via l'API/CLI vers la version du dépôt. Client WS = petit
binaire Go (gorilla/websocket) écrit hors du repo (websocat absent). **Verdict global : ✔** (2 bugs
trouvés et corrigés pendant la vérification, 3 anomalies mineures documentées et renvoyées).

### Résultat par point de checklist

1. **Build & lint** — ✔ `make` complet OK ; `make test` OK (tous les paquets `ok`, pas de FAIL) ;
   `golangci-lint` : aucun problème introduit par la feature. Les 19 issues restantes sont
   **toutes préexistantes sur `main`/HEAD** dans des fichiers non touchés (`main.go`, `config/`,
   `log/format.go`, `models/command.go|network.go|system.go`, et `cmd/calaos-os/api/api.go` +
   `calaos-os.go` déjà présents sur HEAD) → hors périmètre T7 (voir anomalie A3). Link cgo/meson
   propre, pas de symbole dupliqué.
2. **Setup** — ✔ token lu depuis `/run/calaos/calaos-ct.token` (mis en cache au 1er accès dans
   `app/context.go`). Daemon lancé via `LD_LIBRARY_PATH=./bin ./bin/calaos-container -c ./calaos-container.toml`.
3. **/api/events** — ✔
   - `curl` avec token, GET simple → **426** ; sans token → **401** ; token invalide → **401**.
   - WS en-tête `Authorization: Bearer` **et** variante `?token=` → handshake 101 + snapshot
     immédiat `{"type":"upgrade_progress","data":{"status":"idle","total_percent":0}}` ; pings
     serveur reçus (PING toutes 30 s). Mauvais token → handshake 401.
4. **Upgrade réel avec progression** — ✔ (single **et** upgrade-all). `GET /api/update/check`
   (l'endpoint est **GET**, pas POST — cf. anomalie A4) fait remonter `libtasn1-6` dans
   `/api/update/available`. Séquence WS observée (extrait single-package, après corrections) :
   ```
   {"status":"idle","total_percent":0}
   {"status":"upgrading","total_percent":0}
   {"status":"upgrading","current_step":"pending","pkg_index":1,"pkg_count":1,"total_percent":0,"packages":[{"name":"libtasn1-6","state":"pending","percent":0}]}
   {"status":"upgrading","current_step":"download","total_percent":0,"packages":[{"name":"libtasn1-6","state":"pending","percent":0}]}
   {"status":"upgrading","current_step":"unpack","current_percent":20,"total_percent":40,"packages":[{"name":"libtasn1-6","state":"unpacking","percent":20}]}
   {"status":"upgrading","current_step":"install","current_percent":40,"total_percent":64,"packages":[{"name":"libtasn1-6","state":"installing","percent":40}]}
   {"status":"upgrading","current_step":"configure","current_percent":60,"total_percent":64,"packages":[{"name":"libtasn1-6","state":"installing","percent":60}]}
   {"status":"upgrading","current_step":"done","current_percent":100,"total_percent":88,"packages":[{"name":"libtasn1-6","state":"done","percent":100}]}
   {"status":"upgrading","current_step":"done","total_percent":100,"packages":[{"name":"libtasn1-6","state":"done","percent":100}]}
   {"status":"done","total_percent":100,"packages":[{"name":"libtasn1-6","state":"done","percent":100}]}
   {"status":"idle","total_percent":0}
   ```
   `pending → download → unpack → configure → done` respecté ; **`total_percent` monotone**
   0→40→64→64→88→100 ; `pkg_count`=1 stable, `packages[]` cohérent ; snapshot `GET /api/update/status`
   pendant l'opération cohérent avec les events. Paquet effectivement mis à niveau
   (4.19.0-2 → 4.19.0-2+deb12u1). `upgrade-all` : même flux, OK.
5. **Cas d'erreur** — ✔ Fetch cassé via proxy apt mort (`Acquire::http::Proxy 127.0.0.1:9`, cache
   `/var/cache/apt/archives` vidé) → event terminal `status:"error"` publié puis retour `idle` ;
   POST répond 500 `{"error":true,"msg":...}`. Un **2e upgrade dans le même process** repasse
   proprement par toute la machine à états (pas de « already in progress », pas de deadlock de lock)
   → sémaphore et pile `_error` bien purgés. Après retrait du proxy + restart, upgrade de
   récupération **réussit**. NB : message d'erreur générique « unknown APT error » (anomalie A2).
6. **CLI** — ✔ `calaos-os check-update` (table), `calaos-os upgrade libtasn1-6` et `calaos-os upgrade`
   (all) : barre de progression globale + étapes par paquet (pending/download/unpack/install/
   configure/done), `✔ Done.`, **exit 0**. Contre un « vieux » daemon (stub renvoyant 404 sur
   `/api/events`) → message **« le daemon calaos-container est trop ancien … Redémarrez le service »**
   + **exit 1**.
7. **Concurrence** — ✔ `GET /api/update/check` pendant un upgrade → `{"error":true,"msg":"upgrade
   already in progress"}` (10/10 tentatives), l'upgrade se termine ensuite normalement. Sémaphore
   inchangé.
8. **Shutdown** — ✔ SIGTERM au daemon avec un client WS connecté → **close frame propre 1001
   (CloseGoingAway) « server shutting down »**, pas de blocage. `NewVersions` rafraîchi après
   upgrade : `libtasn1-6` disparaît de `/api/update/available` (retourne `{}`).
9. **Journald/logs** — ✔ Sortie dpkg visible sur stdout du daemon (`Preparing to unpack…`,
   `Unpacking…`, `Setting up…`, `Processing triggers…`) grâce à `DPkg::Use-Pty=false`. Aucun prompt
   conffile (confold/confdef forcés dans `aptInit`). `libtasn1-6` n'a pas de conffile, donc le prompt
   conffile lui-même n'a pas pu être déclenché directement, mais aucun blocage n'est survenu.

### Bugs trouvés ET corrigés pendant T7

- **B1 — Deadlock du lock dpkg (bloquant, corrigé).** `apt/apt-install.cpp` : `AptCacheFile::Open(true)`
  prend le lock interne `/var/lib/dpkg/lock` en plus du frontend lock, puis `DoInstall` lance `dpkg`
  qui échoue avec `dpkg: error: dpkg database lock was locked by another process with pid <daemon>` →
  tout upgrade réel échouait (`Sub-process /usr/bin/dpkg returned an error code (2)`). **Correctif** :
  encadrer `pm->DoInstall(...)` par `_system->UnLockInner()` / `_system->LockInner()` (release du lock
  interne le temps du run dpkg, frontend lock conservé). Re-testé : upgrade réel réussit.
  → *concerne T1 (couche C++/CGO).*
- **B2 — Nom de paquet arch-qualifié dédoublé (corrigé).** Le C++ annonce le paquet par son nom nu en
  `pending`/`download` (`libtasn1-6`) mais dpkg le rapporte arch-qualifié en unpack/configure
  (`libtasn1-6:amd64`). `applyAptProgress` les traitait comme 2 paquets distincts → `pkg_count`
  passait à 2 et `packages[]` contenait un doublon (une entrée figée en `downloading`). **Correctif** :
  `normalizePkgName()` dans `models/update.go` retire le suffixe `:arch` avant matching/enregistrement.
  Re-testé : `pkg_count`=1, une seule entrée qui progresse pending→…→done.
  → *concerne T1/T3 (émission du nom côté C++ / mapping Go).*

### Anomalies mineures (documentées, renvoyées — non bloquantes)

- **A1 — Snapshot initial `status:""` au lieu de `"idle"` (corrigé ici).** Au démarrage, `SafeStatus`
  restait à sa valeur zéro : `GET /api/update/status` et le snapshot WS renvoyaient
  `{"status":"","total_percent":0}` au lieu de `"idle"` attendu par la spec (T2/T4). **Correctif
  appliqué** : appel de `resetStatus()` dans `models.Init()` (`models/models.go`). Vérifié : snapshot
  = `{"status":"idle",...}`. → *à confirmer côté T2 (init du hub/Status).*
- **A2 — Message d'erreur générique.** En cas d'échec de fetch, l'event terminal porte
  `error:"unknown APT error"` (la pile `_error` est vide car les erreurs d'item passent par
  `Fail()`/event `download` sans `_error->Error`). Le contrat error est respecté (status:"error"),
  seul le libellé est peu informatif. → *à renvoyer à T1 (`CalaosAcquireStatus::Fail` / `doCommit`).*
- **A3 — Lint global non propre (préexistant, hors périmètre).** 19 issues golangci-lint, toutes sur
  du code présent sur `main`/HEAD non touché par la feature (unused vars couleur, errcheck sur
  `*.Close()`, staticcheck). Aucune introduite par T1–T7. → *nettoyage éventuel hors de cette
  feature.*
- **A4 — Écart doc vs implémentation `/api/update/check` (GET vs POST).** La checklist mentionne
  `POST /api/update/check`, mais l'endpoint est enregistré en **GET** (cohérent avec le client CLI
  `api.go`). Ce n'est pas un bug (daemon et CLI sont cohérents), juste une imprécision de la
  checklist. → *doc à aligner.*

### Fichiers modifiés par T7 (correctifs B1, B2, A1)

- `apt/apt-install.cpp` — `_system->UnLockInner()/LockInner()` autour de `DoInstall` (B1).
- `models/update.go` — `normalizePkgName()` + normalisation de `p.Pkg` dans `applyAptProgress` (B2).
- `models/models.go` — `resetStatus()` dans `Init()` (A1).

État apt remis propre en fin de vérification (libtasn1-6 à la version du dépôt, proxy retiré,
`sources` restaurées, `apt-get update` OK, rien d'upgradable en trop) ; daemon arrêté.
