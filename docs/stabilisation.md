# Stabilisation de toutes les fonctions expérimentales

État des corrections et des validations locales au 5 novembre 2026.

**Objectif : Stable pour toutes les fonctions expérimentales. Objectif non
encore atteint.** Les corrections ci-dessous sont exécutées ; les critères
qui nécessitent d'autres plateformes, une vraie recette ou des évolutions
supplémentaires restent ouverts. Aucun label n'est promu pour masquer un écart.

## Corrections et preuves

| Domaine | Modification réalisée | Vérification |
| --- | --- | --- |
| Quotas | Admission/réservation/libération sérialisées ; refus des montants négatifs et débordements individuels ; libération bornée au compte du tenant ; suppression d'un verrou de lecture récursif lors de l'inventaire ; réconciliation d'une lease terminée avant enregistrement de son slot | Le test concurrent dépassait la limite avant correction ; fin/annulation avant enregistrement reproduisaient une fuite ; tests avec détecteur de courses et cas de réservation négative |
| Reprise | Restauration idempotente des slots depuis les leases ouverts, raccordée au démarrage ; les leases existants au-dessus d'une limite abaissée sont conservés et bloquent les nouvelles admissions | Snapshot réel rechargé, double restauration, libération rejouée et admission après drainage |
| CAS distribué | Option `-cas-dir`, ouverture avant écoute, route GET/PUT authentifiée, vérification digest/taille | Configuration invalide refusée ; client non authentifié et contenu altéré refusés ; aller-retour blob correct. Tests locaux, pas de déploiement réel |
| Installation de plugins | Fichier temporaire exclusif, synchronisation et renommage ; suppression du nom temporaire partagé prévisible | Ancien lien `.loading` détournait l'écriture avant correction ; installation concurrente et contenu final entier vérifiés |
| Couches des plugins | Source symbolique et préfixes dangereux refusés ; lectures via `os.Root` ; écriture d'un tar complet avant remplacement de la sortie | Ancienne source symbolique produisait une couche vide ; tests de rejet et conservation de la sortie précédente ; couches CLI déterministes |
| Inspection des langues | Contrat `language-inspection/v1`, lecture historique conservée, refus de version inconnue | Fixtures figées lues à travers un vrai processus et vérification des huit binaires ; contrôle ajouté à la configuration CI |
| .NET | Redirection du cache par `NUGET_PACKAGES`, création du répertoire après restauration réussie même sans dépendance, résolution des racines avant changement de répertoire | Un vrai SDK échouait avec le chemin précédent ; test hors sources NuGet externes, lock et couche vérifiés. Le SDK local est 10, pas une preuve .NET 8 |
| KubeVirt | Propagation du contexte d'annulation jusqu'aux commandes `kubectl`/`virtctl` | Tests de commande annulée et contexte transmis ; aucun cluster contacté |
| TUI | La recherche ne transmet plus ses caractères aux raccourcis de liste ; Ctrl+C quitte le formulaire runtime ; messages de curseur transmis ; aperçu du tag vide normalisé en `latest` comme le résultat | Les cas `q` et Ctrl+C échouaient avant correction ; tests des quatre packages sous détecteur de courses |
| Installeur | Vérification stdin et stdout ; annulation transmise à la commande active et résultat annulé distinct du succès ; binaire compilé/signé dans un fichier temporaire avant remplacement | Test d'une compilation active annulée, et vrais petits builds réussis/échoués/annulés préservant le binaire précédent |

Les corrections d'annulation s'appuient sur `exec.CommandContext`. Elles ne
constituent pas une garantie générale de contrôle de tous les descendants
créés par un outil externe sur chaque OS.

## Couverture et critères restant à atteindre

Les deux matrices de maturité distinguent les niveaux Experimental, Alpha
et Beta sans les fusionner en une mesure unique. Les niveaux Supported du socle ne sont pas implicitement
promus non plus. Les éléments explicitement hors périmètre ne sont pas des
fonctions expérimentales à réimplémenter par défaut.

| Famille | Écart restant avant Stable |
| --- | --- |
| Ruby/PHP/Java/.NET/Rust | Vrais projets avec dépendances et toolchains supportées, gel puis build/run, panne du gestionnaire, reprise et migration. Les fixtures de cache ne prouvent pas la résolution des dépendances |
| Chargement et sandbox des plugins | Recette de mise à jour/retour arrière sur les OS vendus, échecs de ressources et revue de la frontière de confiance ; installation atomique n'implique pas isolation d'un programme arbitraire |
| KubeVirt/containerd | Cluster désigné, RBAC réellement exercé, coupures API, redémarrage du runtime, état après interruption et rollback. `virtctl` n'est pas installé ici |
| Disques historiques | Boot de vrais disques et formats supportés sur plateformes désignées, écritures/persistance/récupération et contrat de migration. Les parseurs testés ne prouvent pas la démarrabilité |
| HVF/KVM/WHPX et sandbox hôte | Recette native par plateforme et parcours, cycle complet boot/arrêt/reprise, isolation et périphériques. WHPX expose actuellement une sonde de disponibilité, pas un backend de cycle de vie complet |
| Distribution/quotas | Recette du binaire en réseau mTLS sur plusieurs nœuds, interruptions et versions mélangées ; stockage/provenance durables, HA si vendue, identité des tenants et limites du CAS partagé ; fairness complète encore à définir/implémenter |
| Formats de projet, plan, freeze, événements, couches sémantiques | Engagement de compatibilité de chaque format avec fixtures historiques, migration/rollback et intégration dans les parcours réels. Le contrat d'inspection ajouté ici ne stabilise pas ces formats par transitivité |
| Pull Registry et `verify-release` | Interopérabilité de pulls réels et erreurs/reprise ; historique et contrat des flags de vérification, de bout en bout avec les preuves émises |
| Observabilité et erreurs typées | Contrat d'export/monitoring exploitable et adoption des erreurs par les frontières pertinentes ; l'existence d'une API interne n'est pas une exploitation complète |
| Binaire scheduler séparé et helpers virtio | Décider et implémenter le contrat encore manquant, puis sa recette ; ne pas présenter les helpers comme un périphérique ou scheduler complet |

PHP/Composer et Maven/Gradle sont absents de l'environnement vérifié. Les essais
réels Rust/Ruby/.NET se limitent à des projets sans dépendances externes. Aucun
boot invité, essai Windows natif ou déploiement cluster n'a été effectué pour
ces changements. Une compilation croisée n'en tient pas lieu.

## Contrôle de passage

Une promotion exige : contrat versionné, parcours utilisateur réel, cas négatifs,
injection de panne, reprise/rollback, observabilité, documentation d'exploitation,
responsable de maintenance et preuves sur la révision candidate. Les frontières
critiques demandent une revue indépendante de sécurité, distincte des tests automatisés et des revues internes. Les exécutions CI doivent être observées,
pas seulement présentes dans un fichier YAML.

Les validations locales et leurs commandes/résultats sont conservées dans
`.cache/stabilisation/`. Le script versionné
`scripts/ci/verify-language-contracts.py` permet de répéter les contrats des
binaires sans lancer de gestionnaire de dépendances ; il est raccordé au job
qualité après construction des binaires Linux. Sa portée est volontairement
explicite dans sa sortie JSON.

## Résultat des contrôles locaux

- Build, vet et tests réussis sur le module racine et les douze modules secondaires.
- Détecteur de courses réussi sur SDK, quotas, contrôle-plane, installeur, TUI,
  KubeVirt et .NET pour les suites exécutées ; les autres modules ne sont pas
  implicitement couverts par ce constat race.
- Huit vrais binaires de langage vérifiés par le contrôle CLI hors réseau.
- Gel puis couche réellement réussis pour Rust, Ruby et .NET sur des projets
  sans dépendances externes ; le SDK .NET installé est 10.0.302.
- Cross-build racine réussi pour Linux/amd64, Linux/arm64 et Windows/amd64,
  avec CGO désactivé. Cela n'exerce pas les backends natifs.

Le journal final de la régression de cycle de vie est
`.cache/stabilisation/lifecycle-green.log` (à la racine du cache, pas sous
`checks/`). Les journaux conservent les résultats des commandes exécutées. Les contrôles
de format et les liens locaux des documents modifiés sont vérifiés. Ces résultats ne ferment pas les critères restant à atteindre.
