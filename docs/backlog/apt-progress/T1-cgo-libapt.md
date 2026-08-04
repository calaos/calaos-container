# T1 — Couche C++/CGO : upgrade complet via libapt-pkg avec progression

- **Statut** : reviewed
- **Modèle** : fable — **Effort** : high — **Skills** : —
- **Dépend de** : T0

## Objectif

Remplacer l'exécution d'`apt-get` en sous-processus par une implémentation complète en C++ sur libapt-pkg (fetch + install), avec callbacks de progression remontés en Go via CGO.

Signatures libapt-pkg déjà vérifiées sur libapt-pkg 2.6.1 (`/usr/include/apt-pkg/`) — les re-vérifier au besoin. Note build : **cgo compile directement les `.cpp` du répertoire `apt/`** dans le binaire Go ; la lib meson `libcalaos-apt.so` est buildée en parallèle → tout nouveau `.cpp` doit être ajouté à `apt/meson.build` ET sera automatiquement pris par `go build`. Surveiller le link (les objets cgo priment sur la .so, pas d'erreur attendue).

## Spec

### API C exportée — `apt/apt.h`
```c
// stage: "pending"|"download"|"unpack"|"configure"|"install"|"done"|"error"
typedef void (*AptProgressCb)(const char *stage, const char *pkg,
                              int pkg_percent, int total_percent, const char *msg);
void aptSetProgressCallback(AptProgressCb cb);   // pointeur global, NULL = no-op
int aptUpgradeAll(char **errorMsg);              // bloquant, 0=succès; *errorMsg = strdup(pile _error), à free()
int aptInstallPackage(const char *pkgName, char **errorMsg);
```
Enregistrement par pointeur (pas d'`extern goAptProgress` en dur) pour que la .so meson reste linkable seule. API existante (`aptCacheGetArray`...) intacte.

### `apt/apt-progress.{h,cpp}` (nouveaux)
- **`CalaosAcquireStatus : pkgAcquireStatus`** (download) : `MediaChange` → `false` ; `Pulse(Owner)` appelle la base (membre `Percent` = % download global pondéré octets+items) puis itère les workers (`Owner->WorkersBegin()` / `WorkerStep`, `W->CurrentItem->CurrentSize/TotalSize`) pour le % par item — `ShortDesc` = nom du paquet pour les `.deb` ; `Fetch`/`Done`/`Fail` émettent début/fin/erreur d'item ; `MorePulses = true` dans le ctor ; `Pulse` retourne `true`.
- **`CalaosInstallProgress : APT::Progress::PackageManager`** (install) : `StatusChanged(pkg, StepsDone, TotalSteps, action)` → % install global = `StepsDone*100/TotalSteps` ; étape/% par paquet mappés par **préfixe** de `action` (locale forcée `LC_ALL=C`) :
  | Préfixe | stage | pkg_percent |
  |---|---|---|
  | `Preparing to configure` (tester AVANT `Preparing`) | configure | 60 |
  | `Preparing` | unpack | 20 |
  | `Unpacking` | unpack | 40 |
  | `Configuring` | configure | 80 |
  | `Installed` | done | 100 |
  | `Removing` / autre (triggers…) | install | -1 |
  `Error(...)` → stage error ; `ConffilePrompt` → log seulement (confold forcé). Ne PAS overrider `fork()`/`StartDpkg` pty (géré par `pkgDPkgPM`).
- Singleton d'état (`callback`, `downloadWeight`, `lastTotal`) + émetteur `aptEmitProgress(stage, pkg, pkgPct, totalPct, msg)` avec **garde de monotonie** sur le % global (`total = max(last, new)`).
- **Composition % global : fenêtre fixe 40% download / 60% install** ; si `fetcher.FetchNeeded() == 0` → install couvre 0→100.

### `apt/apt-install.cpp` (nouveau)
`doCommit(AptCacheFile&, char **errorMsg)` commun :
1. Émettre `stage="pending"` pour chaque paquet marqué (`state.NewInstall()/Upgrade()/Downgrade()/Delete()`), version candidate dans `msg` → liste exacte de la transaction avant le début.
2. `pkgRecords recs(*cache)` ; `CalaosAcquireStatus acqStatus` ; `pkgAcquire fetcher(&acqStatus)` + `fetcher.GetLock(_config->FindDir("Dir::Cache::Archives"))` ; `std::unique_ptr<pkgPackageManager> pm(_system->CreatePM(cache.GetDepCache()))` ; `pm->GetArchives(&fetcher, cache.GetSourceList(), &recs)`.
3. `fetcher.Run(500000)` ; vérifier chaque item `StatDone`, sinon `_error->Error(...)` + fail.
4. `CalaosInstallProgress instProgress` ; `pm->DoInstall(&instProgress)` ; échec si `Failed`/`Incomplete`/`_error->PendingError()`.
5. `emit("done", "", 100, 100, "")` ; retour 0.

- `aptUpgradeAll` : `AptCacheFile cache; cache.Open(true)` (lock) ; `CheckDeps(false)` ; `cache.DistUpgrade()` (marquage existant) ; si `InstCount()+DelCount()==0` → done immédiat ; sinon `doCommit`.
- `aptInstallPackage` : `FindPkg(name)` (erreur si absent) ; `MarkInstall(P, /*AutoInst*/true, 0, /*FromUser*/true)` ; `pkgProblemResolver fix` + `fix.Protect(P)` + `fix.Resolve(true)` ; erreur si `BrokenCount() != 0` ; puis `doCommit`.
- `fail(errorMsg)` : vide la pile `_error` (`PopMessage` en boucle), `*errorMsg = strdup(...)`, émet `stage="error"` avec le message, retourne 1. Le destructeur `AptCacheFile` fait déjà Close + `_error->Discard()`.

### `apt/apt.cpp` — `aptInit()` enrichi
```cpp
_config->Set("Dpkg::Options::", "--force-confdef");
_config->Set("Dpkg::Options::", "--force-confold");
_config->Set("DPkg::Use-Pty", "false");     // sortie dpkg → stdout/journald
setenv("DEBIAN_FRONTEND", "noninteractive", 1);
setenv("APT_LISTCHANGES_FRONTEND", "none", 1);
setenv("LC_ALL", "C", 1);                   // mapping fiable des actions dpkg
```
Nettoyer les `cout` existants.

### Bindings Go — `apt/apt.go` (modifié) + `apt/callbacks.go` (nouveau)
- `callbacks.go` : `//export goAptProgress` — **fichier séparé obligatoire** (règle cgo : pas de définitions C dans le préambule d'un fichier `//export`) ; préambule limité à `#include "apt.h"`.
- `apt.go` : shim C `static void progressBridge(...)` dans le préambule (appelle `goAptProgress`), `C.aptSetProgressCallback((C.AptProgressCb)(progressBridge))` dans `init()`, `#cgo CXXFLAGS: -std=c++17` si besoin, et :
```go
type Progress struct {
    Stage        string // pending|download|unpack|configure|install|done|error
    Pkg          string
    PkgPercent   int    // -1 = inconnu
    TotalPercent int
    Msg          string
}
func UpgradeAll(cb func(Progress)) error
func InstallPackage(name string, cb func(Progress)) error
```
Var package `progressCb func(Progress)` + mutex, positionnée avant l'appel cgo, remise à nil en defer. Conversion `char **errorMsg` → `error` (+ `C.free`). Le callback est appelé **synchroniquement** (dpkg/download suspendu pendant) → ne jamais bloquer dedans (documenter). Aucun pointeur Go ne traverse la frontière.
- `apt/meson.build` : ajouter `'apt-install.cpp', 'apt-progress.cpp'`.

### Pièges runtime (décisions actées)
- fork/exec dpkg depuis Go : OK sans `runtime.LockOSThread` (appel cgo bloquant déjà épinglé ; `waitpid` sur PID précis, pas de collision os/exec). Vigilance SIGURG/EINTR (apt gère EINTR) ; fallback documenté en commentaire : `GODEBUG=asyncpreemptoff=1`.
- Locks : dpkg lock via `Open(true)` + lock archives gardés toute la transaction — voulu. Pas de conflit avec le `apt-get update -qq` de `checkForUpdates` (lock lists distinct + sémaphore Go).

### Tests — `apt/apt_test.go` (nouveau, CGO)
- Le bridge `goAptProgress` convertit `C.char*`/`C.int` → `apt.Progress` et invoque le callback enregistré.
- Smoke-test `GetCachePackages()` ne crashe pas.

## Fichiers touchés

- **créés** : `apt/apt-progress.h`, `apt/apt-progress.cpp`, `apt/apt-install.cpp`, `apt/callbacks.go`, `apt/apt_test.go`
- modifiés : `apt/apt.h`, `apt/apt.cpp`, `apt/apt.go`, `apt/meson.build`

## Critères d'acceptation

- `make` complet passe (meson + cgo, pas de symbole dupliqué au link).
- `make test` passe (`./apt` inclus).
- `aptUpgradeAll`/`aptInstallPackage` compilent et les events sortent dans l'ordre pending → download → unpack/configure → done avec % global monotone (vérifiable en T7 ; ici au minimum une compilation propre + tests du bridge).
- Ne PAS toucher `models/` (fait en T3).

## Implémentation

Réalisée conformément à la spec (libapt-pkg 2.6.1, signatures re-vérifiées dans `/usr/include/apt-pkg/` avant écriture).

**Fichiers créés** : `apt/apt-progress.h`, `apt/apt-progress.cpp`, `apt/apt-install.cpp`, `apt/callbacks.go`, `apt/apt_test.go`.
**Fichiers modifiés** : `apt/apt.h` (API C + callback), `apt/apt.cpp` (`aptInit` enrichi, `cout` nettoyés), `apt/apt.go` (shim `progressBridge`, `Progress`, `UpgradeAll`/`InstallPackage`), `apt/meson.build` (2 nouveaux .cpp).

**API C finale (`apt/apt.h`)** :
```c
typedef void (*AptProgressCb)(const char *stage, const char *pkg,
                              int pkg_percent, int total_percent, const char *msg);
void aptSetProgressCallback(AptProgressCb cb);
void aptEmitProgress(const char *stage, const char *pkg,
                     int pkg_percent, int total_percent, const char *msg);
int aptUpgradeAll(char **errorMsg);
int aptInstallPackage(const char *pkgName, char **errorMsg);
```

**API Go finale (pour T3)** :
```go
type Progress struct {
    Stage        string // pending|download|unpack|configure|install|done|error
    Pkg          string
    PkgPercent   int    // -1 = inconnu
    TotalPercent int    // 0-100, monotone
    Msg          string
}
func UpgradeAll(cb func(Progress)) error
func InstallPackage(name string, cb func(Progress)) error
```
Les transactions sont sérialisées par un mutex package (`transactionMu`) ; `cb` peut être nil ; `cb` est appelé **synchroniquement depuis le thread cgo** (download/dpkg suspendu) → ne jamais bloquer dedans.

**Points de conformité** :
- `callbacks.go` (fichier `//export goAptProgress`) : préambule limité à `#include "apt.h"` ; le shim C `static progressBridge` + `aptRegisterProgressBridge` sont dans le préambule de `apt.go`.
- Enregistrement par pointeur : `libcalaos-apt.so` n'a aucun symbole Go non résolu (vérifié `ldd -r`/`nm -D`).
- Monotonie : garde `total = max(last, new)` dans `aptEmitProgress` ; fenêtre 40/60 via `AptProgressState::downloadWeight`, mis à 0 si `fetcher.FetchNeeded() == 0` (install couvre 0→100) ; reset en début de transaction.
- `aptInit` : `--force-confdef/--force-confold`, `DPkg::Use-Pty=false`, `DEBIAN_FRONTEND=noninteractive`, `APT_LISTCHANGES_FRONTEND=none`, `LC_ALL=C`.
- Tests : `TestProgressBridge` (chemin complet `aptEmitProgress` C → `progressBridge` → `goAptProgress` → callback, conversions + garde de monotonie), `TestProgressBridgeNoCallback`, `TestGetCachePackagesSmoke`.

**Écarts motivés vs spec** :
1. `Fail()` (échec d'un item de download) émet `stage="download"` avec `msg="fetch failed: …"` et non `stage="error"` : un item peut être retenté depuis une autre source, `error` doit rester réservé à l'échec définitif de la transaction (émis par `fail()` dans `doCommit`). Les `Fail` sur items `StatIdle`/`StatDone` sont ignorés (pattern AcqTextStatus).
2. `aptEmitProgress` est exposée dans `apt.h` (pas seulement interne) pour permettre au test Go d'exercer le pont complet sans fonction C de test dédiée (cgo interdit dans les `_test.go`, helper `emitProgressForTest` dans `apt.go`).
3. `fetcher.Run()` : le code vérifie ensuite chaque item (`StatDone && Complete`) et accumule les erreurs via `_error->Error(...)` avant `fail()` — conforme au point 3 de la spec ; `Incomplete` (media requis) est traité comme une erreur.
4. Ajout d'un event de fermeture de fenêtre download (`aptEmitDownload("download","",-1,100,"")`) après un fetch réussi pour garantir la continuité du % global à l'entrée de la phase install.

Vérifs : `make` complet OK (meson + cgo + CLI, pas de symbole dupliqué), `make test` OK (`./apt` inclus), `gofmt` propre, `golangci-lint run` → 0 issue sur `apt/`.

## Review

**Verdict : OK (reviewed)** — 1 bug réel trouvé et corrigé, 1 artefact de build rafraîchi, 2 remarques mineures acceptées sans correction.

**Points vérifiés** (signatures contrôlées contre `/usr/include/apt-pkg/` de libapt-pkg 2.6.1) :
- *Mémoire* : `*errorMsg` initialisé à `NULL` en entrée des deux fonctions, `strdup` unique dans `fail()`, `free` côté Go (`runTransaction`) ; `CString`/`free` appariés (`InstallPackage`, `emitProgressForTest`) ; les `std::string` temporaires passées en `c_str()` au callback sont vivantes pendant l'appel (callback synchrone, `GoString` copie) ; aucun pointeur Go ne traverse la frontière.
- *Locks* : dpkg lock via `Open(true)` relâché par `~AptCacheFile` → `pkgCacheFile::Close()` → `_system->UnLock` sur tous les chemins (RAII) ; lock archives relâché par `~pkgAcquire` ; `fail()` purge la pile `_error` (`PopMessage`) et `AptCacheFile::Close()` fait `_error->Discard()` → un second appel après échec repart proprement (`resetTransaction()` remet aussi `downloadWeight=40` et `lastTotal=0`).
- *Frontière CGO* : `callbacks.go` sans définition C dans le préambule (règle `//export` respectée) ; shim `progressBridge` dans `apt.go` ; enregistrement par pointeur → `libcalaos-apt.so` sans symbole Go (`nm -D` : 0 undefined via `ldd -r`, exports `aptUpgradeAll`/`aptInstallPackage`/`aptSetProgressCallback`/`aptEmitProgress` présents) ; `cb == nil` sûr (`goAptProgress` teste `progressCb` sous mutex) ; transactions sérialisées par `transactionMu` ; singleton C++ à init thread-safe (static local C++11), accédé par une seule transaction à la fois.
- *Progression* : garde de monotonie `max(last, new)` + clamp 0-100 dans `aptEmitProgress` ; fenêtre 40/60 correcte, `downloadWeight=0` si `FetchNeeded()==0` (install couvre 0→100, cohérent avec les events `pending` à total 0) ; event de fermeture de fenêtre download avant `DoInstall` → transition continue (install démarre à `downloadWeight`) ; mapping des préfixes dpkg vérifié contre les chaînes de `dpkgpm.cc` (LC_ALL=C), `Preparing to configure` bien testé avant `Preparing`.
- *Logique apt* : séquence `Open(true)`/`CheckDeps(false)`/marquage/`GetArchives`/`Run(500000)`/`DoInstall` conforme ; items vérifiés `StatDone && Complete` sinon `_error->Error` + `fail` ; `Incomplete` traité en erreur ; `Worker::CurrentItem` (`QItem : ItemDesc`) → `CurrentSize/TotalSize/ShortDesc` corrects ; `Fail()` ignore `StatIdle`/`StatDone` (pattern AcqTextStatus) ; court-circuits `InstCount()+DelCount()==0` corrects. Les 3 écarts motivés sont justifiés.
- *Build/tests* : `make` complet OK, `make test` OK, `go test ./apt/` OK, `golangci-lint` 0 issue sur `apt/`.

**Anomalies** :
1. **Bug corrigé** (`apt/apt-install.cpp`) : `aptInstallPackage` sur un paquet présent dans le cache mais sans version installable (paquet purement virtuel, ou versions toutes épinglées) → `MarkInstall` était un no-op silencieux, `BrokenCount`/`InstCount` restaient à 0 et la fonction retournait **succès « already installed »**. Ajout d'un garde `CandidateVer == nullptr && CurrentVer == 0` → erreur « no installation candidate ».
2. **Artefact rafraîchi** : les copies `bin/libcalaos-apt.so*` étaient périmées (aucune règle make ne les régénère ; `make` installe dans `/usr/lib`) et n'exportaient pas la nouvelle API — recopiées depuis `apt/build/`. Sans impact runtime (cgo compile les .cpp dans le binaire, et le runtime charge `/usr/lib`), mais `-L./bin` les référence au link.
3. *Mineur, accepté* : les items `StatIdle` restants après `fetcher.Run()` sont traités en échec (apt-get les traite en « transient ») — conservateur, acceptable pour un daemon sans retry.
4. *Mineur, accepté* : les tests du bridge partagent `lastTotal` C++ entre eux → ils supposent l'ordre de déclaration (défaut de `go test`) ; `-shuffle=on` casserait `TestProgressBridge`.
