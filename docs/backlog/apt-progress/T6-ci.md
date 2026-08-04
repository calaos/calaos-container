# T6 — CI : job de tests en gate avant la release

- **Statut** : reviewed
- **Modèle** : haiku — **Effort** : low — **Skills** : —
- **Dépend de** : T0 (cible `make test`)

## Objectif

Chaque push sur `main` publie un .deb en release : aucun paquet ne doit plus sortir sans tests verts.

## Spec

`.github/workflows/build-deb-push.yml` : ajouter un job **`test`** avant le job existant, qui devient dépendant :

```yaml
jobs:
  test:
    name: Run unit tests
    runs-on: ubuntu-latest
    steps:
      - name: Checkout the repo
        uses: actions/checkout@v4
      - name: Run tests
        uses: addnab/docker-run-action@v3
        with:
          image: ghcr.io/calaos/debian_deb_build_base
          options: -v ${{ github.workspace }}:/work
          run: cd /work && make test

  build-and-push-deb-pkg:
    needs: test
    ...
```

- L'image `ghcr.io/calaos/debian_deb_build_base` contient déjà Go 1.26.5, libapt-pkg-dev et meson (elle build le .deb) — `make test` y build la lib apt (dépendance `build-lib`) puis lance `go test ./...`.
- Vérifier le répertoire de travail attendu par l'image (le script `/build-deb.sh` utilise `/work`) ; adapter `cd /work` si besoin.
- Ne pas toucher au reste du workflow (bump version, release, webhook).

## Fichiers touchés

- modifié : `.github/workflows/build-deb-push.yml`

## Critères d'acceptation

- YAML valide (`gh workflow view` ou parse yq/python).
- Le job `build-and-push-deb-pkg` a `needs: test`.
- `make test` passe localement dans les mêmes conditions (le devcontainer est équivalent).

## Implémentation

Job `test` ajouté avant `build-and-push-deb-pkg` dans `.github/workflows/build-deb-push.yml` :
- Checkout du code
- Exécution en docker-run-action avec l'image de build (ghcr.io/calaos/debian_deb_build_base)
- Montage du workspace à `/work` (conforme au script /build-deb.sh)
- Commande : `cd /work && make test`
- Dépendance `needs: test` ajoutée à build-and-push-deb-pkg
- YAML valide, reste du workflow intact (bump, release, webhook)

## Review

**VERDICT : APPROUVÉ** — Implémentation correcte et complète.

**Points vérifiés :**
1. ✓ Job `test` ajouté (lignes 19-30) : Checkout + docker-run-action avec l'image `ghcr.io/calaos/debian_deb_build_base` + montage `/work` + commande `cd /work && make test`.
2. ✓ Job `build-and-push-deb-pkg` (ligne 35) : `needs: test` correctement présent, bloque la release jusqu'aux tests verts.
3. ✓ Git diff : seul le job `test` ajouté + `needs` sur build-and-push-deb-pkg ; bump version/release/webhook intacts.
4. ✓ Makefile cible `test` : dépend de `build-lib`, exécute `CGO_LDFLAGS="-L./bin -lcalaos-apt" go test ./...` — conforme.
5. ✓ debian/control Build-Depends : contient `meson`, `libapt-pkg-dev`, `golang` — image de build dispose de toutes dépendances.
6. ✓ Répertoire `/work` : cohérent avec docker/build-deb.sh (ligne 5 : `workdir="/work"`).
7. ✓ Déclenchement workflow : push main + dispatch, tests gate avant release.
8. ✓ YAML valide : structure correcte (confirmé par grep parsing).
