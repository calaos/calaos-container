# T0 — Dépendances Go & cible make test

- **Statut** : reviewed
- **Modèle** : sonnet — **Effort** : low — **Skills** : —
- **Dépend de** : —

## Objectif

Préparer le socle : mise à jour des dépendances Go nécessaires au WebSocket et réactivation de la cible `make test`.

## Spec

1. `go.mod` / `go.sum` :
   - Bump `github.com/gofiber/fiber/v2` v2.51.0 → **v2.52.x** (requis par contrib/websocket ≥ v2.52.6).
   - Ajouter `github.com/gofiber/contrib/websocket` v1.3.4 (daemon) et `github.com/gorilla/websocket` v1.5.3 (CLI).
   - `go get` puis `go mod tidy`. Les imports ne sont pas encore utilisés : soit les ajouter au moment du tidy dans les tasks suivantes, soit les garder via un `//nolint`/import blank temporaire — préférer simplement les ajouter en `require` explicites (`go get` les garde même sans usage tant qu'on ne re-tidy pas ; sinon documenter que T2/T4/T5 feront le tidy final).
2. `Makefile` : la cible `test` (actuellement `@echo test disabled`) devient :
   ```makefile
   test: build-lib ## Run the tests of the project
   	CGO_LDFLAGS="$(SERVER_LDFLAGS)" $(GOTEST) ./...
   ```
   (le package `apt` est CGO ; les packages qui l'importent en ont besoin pour compiler les tests).

## Fichiers touchés

- `go.mod`, `go.sum`, `Makefile`

## Critères d'acceptation

- `make` complet passe (lib C++ + daemon + CLI) après le bump fiber.
- `make test` s'exécute (0 test aujourd'hui = OK, il ne doit plus afficher « test disabled »).
- Aucun changement de code applicatif.

## Implémentation

- `github.com/gofiber/fiber/v2` bump v2.51.0 → **v2.52.14** (dernière patch v2.52.x disponible au moment de l'implémentation).
- Ajout de `github.com/gofiber/contrib/websocket v1.3.4` et `github.com/gorilla/websocket v1.5.3` via `go get` (require explicites, marqués `// indirect` par go puisque non encore importés dans le code).
- **`go mod tidy` volontairement NON exécuté.** Testé dans une copie de travail : `tidy` retire les trois nouvelles deps websocket (`gofiber/contrib/websocket`, `gorilla/websocket`, `fasthttp/websocket` transitive) car aucun fichier Go ne les importe encore, et fait aussi apparaître `github.com/godbus/dbus/v5` comme non utilisé (préexistant, sans rapport avec cette task — non touché). Les tasks T2/T4/T5 (qui introduisent l'usage réel du WebSocket) devront lancer `go mod tidy` une fois les imports en place.
- `Makefile` : cible `test` remplacée par `test: build-lib` + `CGO_LDFLAGS="$(SERVER_LDFLAGS)" $(GOTEST) ./...`, conforme au style existant (mêmes variables que `build-server`).
- Vérifications : `make clean && make` passe (lib C++ meson + daemon CGO + CLI) ; `make test` s'exécute réellement et affiche `[no test files]` pour tous les packages (0 test aujourd'hui = attendu) — le message « test disabled » a disparu.
- `gofmt -l .` signale uniquement `models/structs/network.go`, fichier non modifié par cette task (pré-existant, hors scope).
- Aucun code applicatif touché ; rien commité (conformément à la consigne — commit final prévu après T8).

## Review

**Verdict : OK.**

Points vérifiés :

- `git diff go.mod Makefile` conforme exactement à la spec : `fiber/v2` v2.51.0 → v2.52.14 (dans la plage v2.52.x demandée), ajout de `github.com/gofiber/contrib/websocket v1.3.4` et `github.com/gorilla/websocket v1.5.3` en `require // indirect` (plus la transitive `fasthttp/websocket v1.5.8` et quelques bumps indirects induits par fiber v2.52). `git diff --name-only` limité à `go.mod`, `go.sum`, `Makefile` — aucun fichier applicatif touché par cette task (d'autres fichiers modifiés dans l'arbre de travail — `models/structs/status.go`, `.github/workflows/build-deb-push.yml` — proviennent des tasks T2/T6 exécutées en parallèle, hors scope T0).
- Cible `test` du Makefile : `test: build-lib` + `CGO_LDFLAGS="$(SERVER_LDFLAGS)" $(GOTEST) ./...`, exactement conforme à la spec et au style de `build-server` (mêmes variables `SERVER_LDFLAGS`/`GOTEST`).
- `make test` exécuté réellement : build de `libcalaos-apt` OK, puis `go test ./...` sur tous les packages, exit code 0, sortie `[no test files]` partout (aucun test aujourd'hui = attendu), plus de « test disabled ».
- `make` complet exécuté : lib C++ (meson/ninja) + `bin/calaos-container` (16 Mo) + `bin/calaos-os` (10 Mo) générés sans erreur.
- Les 3 nouvelles dépendances sont bien présentes dans `go.mod` (`go list -m all` les confirme : `gofiber/contrib/websocket v1.3.4`, `gorilla/websocket v1.5.3`, `fasthttp/websocket v1.5.8` transitive). `CGO_LDFLAGS="-L./bin -lcalaos-apt" go build ./...` passe (exit 0).
- Vérification du point « `go mod tidy` non exécuté » : reproduit dans une copie de travail isolée (`go mod tidy`) — confirme que les 3 deps websocket ainsi que `github.com/godbus/dbus/v5` disparaissent du `go.mod`, comme documenté dans la section Implémentation. Comportement attendu et correctement anticipé/documenté pour T2/T4/T5.
- `gofmt -l .` ne signale que `models/structs/network.go`, absent du diff de cette task (confirmé par `git diff --name-only`) — hors scope, sans rapport avec T0.

Aucune anomalie trouvée. Aucune correction demandée.
