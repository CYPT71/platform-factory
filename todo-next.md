# Prochaines étapes

Feuille de route consolidée au 5 novembre 2026. Les cases ouvertes décrivent
le travail restant ; elles ne signifient pas que toute la fonction est absente.
L'ordre indique les dépendances et la priorité, sans engagement de date.

Les contrats d'utilisation restent dans [la documentation](docs/README.md).
Les [capacités](docs/reference/maturity.md), le [support des versions](docs/maturity.md)
et le [suivi de stabilisation](docs/stabilisation.md) gardent leurs rôles respectifs.
Cette liste remplace les anciens journaux de patch et les checklists historiques.

## Point de départ vérifié

Sur la révision [`4c16589`](https://github.com/CYPT71/platform-factory/commit/4c165895ba7e9b26eef6ec043abdc0e992300730) :

- [Qualité](https://github.com/CYPT71/platform-factory/actions/runs/42038261909) et
  [sécurité](https://github.com/CYPT71/platform-factory/actions/runs/42038261868)
  ont terminé avec succès.
- Les notices obligatoires des plugins sont restaurées. Le test des transitions
  heartbeat est déterministe ; il ne prouve pas la reprise d'un flux interrompu.
- Les extractions applicatives historiques ne constituent plus un chantier
  systématique. Les frontières sont contrôlées par `internal/archtest` ; toute
  nouvelle extraction doit résoudre un problème concret.

## Prochain lot — fiabilité et parcours principal

- [x] **Reprise du transport invité après timeout.** Coupures au milieu d'un
  en-tête, d'une trame et d'une réponse testées de façon déterministe dans
  `internal/guesttransport` (`recovery_test.go`), avec rejet explicite de toute
  réponse désynchronisée (`protocol_test.go`). Fermé sur
  [`d0095a9`](https://github.com/CYPT71/platform-factory/commit/d0095a9) :
  `TestPartialExchangesRequireANewConnection` (4 points de coupure),
  `TestCodecRejectsTamperingReplayAndOversizedFrames` (rejeu),
  `TestClientRejectsCorrelationMismatchedResponse` (corrélation réponse/requête),
  `TestRecoveryHoldsOverARealOSBackedTransport` (reprise sur un vrai socket Unix
  du noyau, pas `net.Pipe`). Un biais de ce même point avait aussi provoqué une
  régression concrète côté hyperviseur (fermeture précoce de `GuestChannel`
  avortant tout le run KVM avant que la console n'observe l'arrêt réel du
  guest) ; corrigé sur [`97242da`](https://github.com/CYPT71/platform-factory/commit/97242da),
  PR [#17](https://github.com/CYPT71/platform-factory/pull/17).
- [x] **Runtime Linux des langues interprétées.** `plugins/lang-node` et
  `plugins/lang-dotnet` implémentent désormais `runtime` sur le même contrat
  que l'adaptateur Python (image Linux épinglée par digest, fermeture
  d'interpréteur vérifiée et mise en cache, lock rafraîchi), et
  `plugins/lang-go` gagne `freeze`/`build-layer` (seule langue encore à "Non"
  dans le tableau de docs/language-plugin-layers.md). Preuve : commit
  6d38d1f (implémentation + tests), complété par 9c28817 (câblage de la
  commande dotnet manquant). `go build`/`go test ./...` passent sur l'arbre
  complet. Voir [les plugins de langage](docs/language-plugin-layers.md).
- [x] **Parcours projet complet.** `tests/kind/test-kind-python-lifecycle.sh`
  rejoint le niveau du script Go précompilé (build→publish→deploy→scale→
  rollback→redeploy→rollback, vérification des referrers OCI 1.1) pour un
  projet Python construit depuis sa source via `pf init`/`pf freeze`/
  `pf build`, sur un vrai cluster kind. Preuve : 63f9c3f (script + corrections
  support : exclusion de `.platform-factory/keys` et `node_modules/.bin` du
  gel, retry registre), complété par 1fd1316 (découverte des referrers OCI
  1.1 avec repli sur le schéma de tag).
- [x] **Recette de la TUI.** docs/tui-acceptance.md documente la suite
  automatisée et le parcours manuel (clavier seul, petit terminal,
  NO_COLOR/TERM=dumb, annulation sans écriture disque, saisie invalide puis
  reprise). Un vrai blocage trouvé et corrigé : une référence d'image non
  épinglée par digest était acceptée dans runtimetui et échouait seulement
  plus tard, loin du point de saisie ; buildtui/marketplacetui avaient la
  même classe de bug (erreur affichée une fois puis jamais effacée). Preuve :
  4cc33aa.

## Ensuite — compatibilité et exploitation

- [x] **Dépendances réelles des huit plugins de langage.** Tous les huit plugins
  (`go`, `python`, `node`, `dotnet`, `java`, `php`, `ruby`, `rust`) ont été
  exercés avec gestionnaire indisponible, cache incomplet (refus de `build-layer`
  avant `freeze`), lock obsolète/ajout de fichier après gel (`VerifyFreezeInventory`),
  et génération de couches tar reproductibles identiques en SHA-256 :
  - `go` (`go mod tidy` + `go mod vendor` ajoutés, `9c28817`),
  - `python` (fallback `python3`, `f6a2bbf`),
  - `node` (`npm ci --ignore-scripts`, exclusion `node_modules/.bin`),
  - `dotnet` (`dotnet restore --use-lock-file`, cache incomplet + reprise),
  - `php` (`composer:2` réel avec `psr/log 3.0.2`),
  - `java` (`maven:3.9` réel `dependency:go-offline` avec `slf4j-api 2.0.16`),
  - `rust` (`cargo --locked`) et `ruby` (`bundle`).
- [x] **Formats et migrations.** Configuration projet, lock, plan, événements
  et couches sémantiques audités un par un. Preuve sur PR
  [#18](https://github.com/CYPT71/platform-factory/pull/18) :
  - Configuration projet : déjà couvert avant ce lot
    (`internal/project/migrate.go` + `migrate_test.go` : ajout de version
    implicite, refus de version future, préservation des champs/commentaires).
  - Lock : trou réel trouvé et corrigé — `pf freeze` laissait un lock v1
    inchangé pour toujours (ni la branche v2 ni la branche
    « fichier absent » ne le prenaient en charge), sans aucune erreur. Il
    migre maintenant vers v2 en place (`cmd/platform-factory/project.go`).
    Lecture ancienne/nouvelle et refus de version future étaient déjà testés
    (`internal/project/lock_test.go`).
  - Plan (pipeline v1alpha1/v1beta1/v1) : lecture ancienne/nouvelle déjà
    prouvée sur de vraies fixtures JSON (`internal/pipeline/compat_test.go`).
    Trou de test comblé — aucun test n'envoyait une version réellement
    inconnue ; ajouté dans `sdk/pipeline/pipeline_test.go`.
  - Événements (journal de provenance, incidents de hook) : la nouvelle
    identité `platform-factory.dev/journal/v1` n'était testée qu'à travers
    son alias historique `secure-oci.dev/journal/v1` ; aucun test n'envoyait
    une version inconnue dans l'un ou l'autre format
    (`internal/provenance/journal_test.go`, `internal/hooks/test_autofix.py`).
    Les deux trous sont comblés.
  - Couches sémantiques (catégories de dépendances) : déjà couvert avant ce
    lot (`internal/project/config_test.go` :
    `TestConfigRejectsUnknownDependencyCategory`).
  - Retour arrière : pour chaque format, une version plus récente que celle
    comprise par ce binaire est refusée explicitement plutôt qu'interprétée
    au hasard — c'est ce qui rend un retour à une version antérieure de
    platform-factory sûr sur un document déjà écrit par une version plus
    récente.
  Voir [la compatibilité](docs/api-compatibility.md).
- [x] **Installation et mises à jour.** `scripts/local/install.sh` (staging
  atomique, sauvegarde `.pf-backup/latest`, manifeste SHA-256, `--rollback`,
  commit `df57ab0`) et l'archive `bootstrap.sh` + `platform-factory-packager --verify`
  ont été vérifiés en installation propre, échec de mise à jour laissant
  l'ancien binaire intact, mise à jour + rollback, et rejet d'altération sur les
  deux OS cibles retenus : `darwin/arm64` natif et `linux/arm64` (conteneur
  Linux). Windows est explicitement fermé hors périmètre (pas d'hôte Windows
  natif disponible ; Parallels écarté). Voir [la distribution](docs/distribution.md).
- [ ] **Registry et vérification des preuves.** Compléter les essais de pull réel,
  authentification, reprise et interopérabilité ; stabiliser les flags de
  `verify-release`. **Terminé si :** les fournisseurs annoncés passent les mêmes
  scénarios positifs et négatifs avec les preuves produites par le projet.
  **Progrès réel :** retry/backoff sur 429/502/503/504 pour les GET/HEAD
  idempotents et découverte des referrers OCI 1.1 (avec repli sur le schéma de
  tag) ajoutés et exercés par le parcours Python sur kind (commits 63f9c3f,
  1fd1316) ; `verify-release --release-dir` charge le lot de preuves depuis un
  seul répertoire au lieu de cinq flags séparés. Les essais contre de vrais
  fournisseurs de registry externes restent à faire.
- [x] **Observabilité exploitable.** Export Prometheus déterministe des
  métriques (`internal/observability/export.go`, commit c1882f6), rédaction
  des tags/événements de span sensibles avant export ; corrélation par
  `trace_id`/`span_id` et procédure d'exploitation déjà réelles (voir
  `docs/runbooks/incident-response.md`, réécrit pour ne référencer que des
  commandes qui existent vraiment).

## À terme — plateformes et isolation

- [ ] **KubeVirt et containerd en conditions dégradées.** Étendre les scénarios
  existants aux droits RBAC insuffisants, coupures API, redémarrages et nettoyage
  après interruption. **Terminé si :** état, reprise et rollback sont vérifiés
  sur des clusters et versions désignés, avec preuves conservées.
  **Progrès réel :** classification RBAC-refusé/API-indisponible/interrompu
  testée (`degraded_test.go`, commit c820d17, complété par a0e4cff pour
  capturer le stderr réel de kubectl/virtctl) ; le shim containerd annule
  désormais proprement une création de tâche partiellement faite au lieu de
  laisser un rootfs monté sans runtime derrière (commit 32e9c1d). Reste à
  faire : le même parcours sur un vrai cluster désigné avec preuves
  conservées, pas seulement la classification unitaire.
- [ ] **Isolation des plugins et workers.** Définir la politique réseau par
  destination et le confinement réellement appliqué aux processus ; expliciter
  les permissions larges encore nécessaires. **Terminé si :** une recette
  hostile et une revue indépendante vérifient les frontières annoncées.
  **Progrès réel :** allowlist réseau par destination (host/wildcard/CIDR/port,
  fail-closed) et rédaction des variables d'environnement contenant des
  marqueurs de secret avant qu'un plugin ne les voie (commit 73c4877). La
  revue indépendante reste à organiser — son périmètre est maintenant écrit
  dans `docs/architecture-decisions.md` §4.
- [x] **Distribution multi-tenant.** Identité de tenant extraite et vérifiée
  depuis le certificat mTLS (`OU=tenant:<id>` ou SAN SPIFFE), CAS physiquement
  isolé par tenant (répertoires `0700`, quota disque optionnel), anti-usurpation
  sur `submit`/`status`, absence de fuite croisée, reprise durable après
  redémarrage du control-plane, perte de worker (`Reap` + rejet d'une complétion
  obsolète) et flotte à versions/capacités mélangées prouvés avec `-race`
  (`TestMultiTenantMTLSNoCrossTenantLeakQuotaAndRestartRecovery` et
  `TestMultiTenantWorkerLossAndMixedWorkerVersions` dans
  `cmd/platform-factory-control-plane/multitenant_test.go`). La haute
  disponibilité multi-région est explicitement différée (`docs/architecture-decisions.md` §1).
  Voir [les limites actuelles](docs/distributed-builds.md).
- [ ] **Hyperviseurs et disques historiques.** Conserver des recettes natives
  distinctes pour KVM et HVF, puis couvrir boot, arrêt, réseau, persistance et
  récupération des formats de disque annoncés. **Périmètre Windows/WHPX fermé
  hors périmètre** (`docs/architecture-decisions.md` §3 et `docs/reference/maturity.md` :
  sonde uniquement, pas d'hôte Windows natif, piste Parallels écartée). Reste :
  désigner un responsable et une recette native dédiée par cible KVM/HVF.
- [x] **Décisions d'architecture restantes.** `docs/architecture-decisions.md`
  tranche les trois points ouverts avec une base mesurée : pas de scheduler
  séparé (latence micro-seconde observée, risque réseau/synchronisation non
  justifié), pas de périphérique virtio supplémentaire sans contrat de charge
  concret, WHPX hors périmètre. Chaque décision porte son propre déclencheur
  de réexamen explicite plutôt qu'une fermeture définitive.
- [ ] **Maintenance durable.** Désigner les relais de maintenance manquants et
  organiser une revue indépendante des extractions OCI, de la sandbox et de la
  cryptographie. **Terminé si :** responsables, résultats et corrections sont
  publiés sans assimiler les tests internes à une revue externe.
  **Progrès réel :** le périmètre de revue indépendante est maintenant écrit
  (`docs/architecture-decisions.md` §4 : extraction rootfs OCI, sandboxing
  plugin/réseau, signature/mTLS) ; désigner les relais et mener la revue
  elle-même restent à faire — ce ne sont pas des tests internes.

## Règle de clôture

Une tâche se ferme avec une preuve liée à une révision et à un environnement,
un parcours réel, les cas négatifs pertinents, une documentation à jour et un
responsable identifié. Toute promotion de maturité doit mettre à jour sa matrice
avec ces preuves. Les fonctionnalités explicitement hors périmètre ne sont pas
rouvertes automatiquement par cette feuille de route.
