# Marketplace de plugins

Le marketplace Platform Factory suit le modèle des modules Go : il indexe des versions, mais n’héberge pas les plugins. Chaque version reste un tag SemVer immuable dans le dépôt Git de son éditeur.

```text
Dépôts Git → synchronisation de l’index → recherche/TUI → vérification → installation locale
```

Le MVP comprend :

- des sources Git locales, GitHub, GitLab ou compatibles avec `git` ;
- les tags SemVer `vMAJOR.MINOR.PATCH` ;
- un `plugin.yaml` strict à la racine de chaque tag ;
- un index local centralisé, atomique et interrogeable ;
- recherche fuzzy, pagination, filtres et tris ;
- installation d’une version exacte, mise à jour et suppression ;
- vérification du checksum, de la compatibilité et de la signature Ed25519 avant exposition du plugin ;
- une TUI affichant versions, permissions, vérification, état installé et mises à jour.

Le marketplace n’est ni un registry npm, ni un hébergeur de binaires, ni une autorité de confiance globale. L’index pointe toujours vers le dépôt source et son tag.

## Démarrage junior : installer un plugin

Ces commandes fonctionnent depuis un dossier vide ; aucun projet Platform Factory n’est requis.

```sh
pf marketplace sources add https://github.com/acme/pf-python.git
pf marketplace sync --key acme-public.pem
pf marketplace search python --verified
pf marketplace install --key acme-public.pem acme-python@v1.2.0
pf marketplace list
pf marketplace tui --key acme-public.pem
```

La TUI utilise :

- `↑`/`↓` pour naviguer ;
- `Entrée` pour ouvrir un plugin ;
- `Tab` pour alterner pertinence, popularité, vérification, nom et date ;
- `Ctrl-V` pour limiter la liste aux releases vérifiées ;
- `Ctrl-L` pour effacer la recherche ;
- `i` et `u` pour installer ou changer de version ;
- `x`, puis `y`, pour confirmer une suppression ;
- `Échap` pour revenir ou quitter.

Une release non signée est refusée par défaut. `--allow-unsigned` est réservé au développement local explicite ; le checksum reste obligatoire. Une release signée exige une clé `--key`. La TUI bloque ces actions et indique la correction requise avant toute opération réseau.

## Parcours intermédiaire : publier un plugin

Un dépôt minimal contient :

```text
acme-python/
├── plugin.yaml
└── plugin.py
```

```yaml
api_version: platform-factory.dev/marketplace-manifest/v1
name: acme-python
version: v1.2.0
description: Détection et construction des applications Python
author: Acme Platform Team
entrypoint: plugin.py
tags: [python, build]
compatibility: [">=v1.0.0", "<v2.0.0"]
permissions:
  network: [pypi.org]
  filesystem: [workspace:read, output:write]
  secrets: []
```

Le champ `entrypoint` accepte un fichier ou un répertoire source. Son checksum couvre le contenu complet et déterministe. La version du manifeste doit correspondre au tag :

```sh
git add plugin.yaml plugin.py
git commit -m 'release v1.2.0'
git tag v1.2.0
git push origin main v1.2.0
```

Une mise à jour modifie `version`, crée un nouveau commit et un nouveau tag. Un tag existant ne doit jamais être déplacé : l’installation compare le contenu récupéré au checksum mémorisé lors de la synchronisation et refuse toute divergence.

## Parcours sénior : opérer l’écosystème

Les fichiers d’état sont placés sous le répertoire de configuration utilisateur. `PLATFORM_FACTORY_MARKETPLACE_DIR` permet d’isoler un environnement CI, une organisation ou un cache partagé :

```sh
export PLATFORM_FACTORY_MARKETPLACE_DIR="$PWD/.marketplace"
pf marketplace sources add https://git.example.net/platform/python.git
pf marketplace sources add https://git.example.net/platform/security.git
pf marketplace sync --key publisher-public.pem
pf marketplace search --verified --sort popularity --page 1 --page-size 50
```

Garanties opérationnelles :

- décodage strict et borné des manifestes et de l’index ;
- écritures atomiques, permissions locales restrictives et refus des index symlinkés ;
- synchronisation incrémentale des tags SemVer ;
- échec d’un dépôt isolé des autres sources ;
- clone exact du tag avant installation ;
- vérification de compatibilité avec la version de l’hôte ;
- checksum obligatoire et signature Ed25519 vérifiée avec une clé explicitement approuvée ;
- copie d’installation confinée, sans symlink ni fichier spécial.

Un cache-proxy Git peut être placé devant les dépôts sans changer le protocole. Il doit préserver les références et objets Git ; il ne devient pas une nouvelle source de vérité.

## Validation hermétique

Le test CLI `TestMarketplaceExperienceFromEmptyDirectory` construit deux releases dans un vrai dépôt Git temporaire, démarre dans un dépôt utilisateur vide, puis valide source, synchronisation, recherche, installation exacte, mise à jour, liste et suppression. Les tests de `cmd/tui/marketplacetui` pilotent le modèle Bubble Tea sans terminal réel et couvrent recherche, navigation, rendu et installation/suppression.

```sh
go test ./internal/marketplace ./cmd/tui/marketplacetui ./cmd/platform-factory
```
