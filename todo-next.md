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

- [ ] **Reprise du transport invité après timeout.** Reproduire les coupures au
  milieu d'un en-tête, d'une trame et d'une réponse dans `internal/guesttransport`.
  Définir quand reprendre et quand fermer/reconnecter, sans accepter de réponse
  désynchronisée. **Terminé si :** tests déterministes des lectures/écritures
  partielles, du rejeu, de la corrélation et de la reprise sur un vrai transport.
- [ ] **Runtime Linux des langues interprétées.** Partir du chemin Python déjà
  présent, puis couvrir Node.js et .NET avec des sources épinglées, une fermeture
  de bibliothèques vérifiée et des caches réutilisables. **Terminé si :** un projet
  avec dépendances passe init, gel, build et exécution depuis un environnement
  propre, avec refus explicite des plateformes et dépendances non supportées.
  Voir [les plugins de langage](docs/language-plugin-layers.md).
- [ ] **Parcours projet complet.** Vérifier la continuité entre construction,
  publication, déploiement, état, logs et rollback, en réutilisant le digest et
  l'état persisté du projet. **Terminé si :** un scénario Python réel rejoint le
  niveau du [parcours compilé sur cluster](docs/local-kubernetes-acceptance.md),
  avec panne, nouvelle tentative et rollback, sans manifeste recopié à la main.
- [ ] **Recette de la TUI.** Mesurer le parcours installation → initialisation →
  résultat avec des utilisateurs découvrant le projet ; couvrir clavier seul,
  petit terminal, absence de couleur, annulation et reprise après erreur.
  **Terminé si :** les blocages observés sont corrigés, les actions annoncées
  correspondent aux commandes exécutées et une recette reproductible est publiée.

## Ensuite — compatibilité et exploitation

- [ ] **Dépendances réelles des huit plugins de langage.** Exécuter les toolchains
  supportées avec dépendances externes, gestionnaire indisponible, cache incomplet
  et lock obsolète. **Terminé si :** gel et construction sont reproductibles et
  les erreurs/reprises sont vérifiées pour chaque langage annoncé comme supporté.
- [ ] **Formats et migrations.** Prioriser configuration projet, lock, plan,
  événements et couches sémantiques ; figer les fixtures historiques et documenter
  les changements permis. **Terminé si :** lecture ancienne/nouvelle, migration,
  refus de version inconnue et retour arrière passent sur les versions ciblées.
  Voir [la compatibilité](docs/api-compatibility.md).
- [ ] **Installation et mises à jour.** Tester installation propre, mise à jour,
  interruption et retour arrière des archives et plugins sur chaque OS supporté.
  **Terminé si :** intégrité, permissions, confiance et ancien binaire utilisable
  sont contrôlés sur des machines natives. Voir [la distribution](docs/distribution.md).
- [ ] **Registry et vérification des preuves.** Compléter les essais de pull réel,
  authentification, reprise et interopérabilité ; stabiliser les flags de
  `verify-release`. **Terminé si :** les fournisseurs annoncés passent les mêmes
  scénarios positifs et négatifs avec les preuves produites par le projet.
- [ ] **Observabilité exploitable.** Définir l'export des métriques et traces,
  les erreurs publiques et les alertes utiles. **Terminé si :** un incident réel
  peut être diagnostiqué avec identifiants corrélés, sans contenu sensible,
  depuis les [procédures d'exploitation](docs/runbooks/incident-response.md).

## À terme — plateformes et isolation

- [ ] **KubeVirt et containerd en conditions dégradées.** Étendre les scénarios
  existants aux droits RBAC insuffisants, coupures API, redémarrages et nettoyage
  après interruption. **Terminé si :** état, reprise et rollback sont vérifiés
  sur des clusters et versions désignés, avec preuves conservées.
- [ ] **Isolation des plugins et workers.** Définir la politique réseau par
  destination et le confinement réellement appliqué aux processus ; expliciter
  les permissions larges encore nécessaires. **Terminé si :** une recette
  hostile et une revue indépendante vérifient les frontières annoncées.
- [ ] **Distribution multi-tenant.** Définir l'identité des tenants, l'isolation
  du CAS, les quotas disque et la fairness ; exercer perte de worker, redémarrage
  et versions mélangées sur le binaire distribué. **Terminé si :** les scénarios
  mTLS prouvent absence de fuite entre tenants et reprise durable. La haute
  disponibilité reste une décision à prendre. Voir [les limites actuelles](docs/distributed-builds.md).
- [ ] **Hyperviseurs et disques historiques.** Conserver des recettes natives
  distinctes pour KVM et HVF, puis couvrir boot, arrêt, réseau, persistance et
  récupération des formats de disque annoncés. Décider du périmètre Windows
  avant d'implémenter WHPX, qui n'est actuellement qu'une sonde de disponibilité.
  **Terminé si :** chaque cible annoncée dispose d'un responsable et d'une recette
  native sur la révision candidate ; une compilation croisée ne suffit pas.
- [ ] **Décisions d'architecture restantes.** Évaluer le besoin d'un scheduler
  séparé et de périphériques virtio supplémentaires à partir de cas mesurés.
  **Terminé si :** une décision documentée fixe le périmètre ; seules les
  évolutions retenues reçoivent un contrat et une recette.
- [ ] **Maintenance durable.** Désigner les relais de maintenance manquants et
  organiser une revue indépendante des extractions OCI, de la sandbox et de la
  cryptographie. **Terminé si :** responsables, résultats et corrections sont
  publiés sans assimiler les tests internes à une revue externe.

## Règle de clôture

Une tâche se ferme avec une preuve liée à une révision et à un environnement,
un parcours réel, les cas négatifs pertinents, une documentation à jour et un
responsable identifié. Toute promotion de maturité doit mettre à jour sa matrice
avec ces preuves. Les fonctionnalités explicitement hors périmètre ne sont pas
rouvertes automatiquement par cette feuille de route.
