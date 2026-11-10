# Stabilisation de toutes les fonctions expérimentales

État des corrections et des validations locales au 10 novembre 2026.

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
boot invité ni déploiement cluster n'a été effectué pour ces changements.
Les essais Windows ajoutés ensuite sont limités à la recette CLI décrite
ci-dessous. Une compilation croisée n'en tient pas lieu.

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

## Recette locale répétée

Les corrections locales du 5 novembre 2026 ajoutent :

- `pf build --yes` reconnu comme une construction du projet ;
- `PYTHONPATH` configuré dans l'image Python seulement si les dépendances sont
  empaquetées, avec priorité à une valeur explicitement configurée ;
- `pf run -d --name api -p 127.0.0.1:8000:8000` en mode projet. Les restrictions
  habituelles du conteneur restent appliquées. Le mode détaché imprime un
  identifiant et ne garantit pas que le service soit prêt. Ces options sont
  refusées avec `--watch`, et le détachement est réservé à l'isolation conteneur ;
- remplacement atomique du lock produit par le gel : échec ou annulation du
  producteur conserve le fichier précédent ; les cibles symboliques ou hors du
  projet sont refusées. SIGINT/SIGTERM annule le processus actif ; le contrôle
  de tous ses descendants n'est pas garanti ;
- fermeture du transport invité après échange interrompu ou erreur de protocole.
  `ErrReconnectRequired` impose une nouvelle connexion authentifiée. Aucune
  commande n'est rejouée automatiquement : son résultat pourrait être inconnu.
  Une erreur applicative authentifiée ne désynchronise pas le flux. Les tests
  interrompent en-têtes/trames de requête et réponse via `net.Pipe` ; ils ne
  constituent pas un essai de redémarrage d'une microVM réelle.

### Rejouer sans téléchargement

Les deux scripts utilisent uniquement la bibliothèque standard Python. Fournir
les exécutables déjà construits ; les rapports enregistrent leur SHA-256, sans
consulter le contrôle de versions. Le dossier de résultats doit être nouveau.
Les commandes, sorties, refus attendus et tentatives ratées sont conservés.

```bash
python3 scripts/validation/binary-packaging.py \
  --pf /chemin/pf --builder /chemin/oci-builder \
  --binary /chemin/executable-linux-amd64 \
  --results /tmp/pf-packaging-resultats --runs 10
```

Chaque essai construit deux images OCI du même binaire, compare leurs digests,
accepte une copie intacte puis refuse son manifeste modifié. Le script supprime
ses propres dossiers temporaires et conserve les journaux et le rapport.
Il ne compile pas les sources et ne contacte aucun registre ni moteur.

Pour FastAPI, préparer séparément les outils décrits dans [la démo](../demo/README.md),
un moteur déjà actif et une image Python de base déjà en cache, épinglée par
son digest. Le dossier fixture doit contenir `app.py`, `requirements.txt`,
`requirements.lock`, `pf.yaml`, `pf.lock` et le runtime préparé sous
`.platform-factory/deps/python/runtime`. Le wheelhouse local doit contenir toutes
les distributions Python pures verrouillées de la fixture. L'interpréteur
`--python` doit disposer de pip ; conserver son chemin de virtualenv.

```bash
python3 scripts/validation/local-journey.py \
  --pf /chemin/pf --python /chemin/venv/bin/python \
  --fixture /chemin/fixture-fastapi --wheelhouse /chemin/wheels \
  --engine podman --base-image python@sha256:DIGEST_COMPLET_DEJA_EN_CACHE \
  --results /tmp/pf-fastapi-resultats --runs 10 --baseline-runs 3
```

Le script interdit les résolutions pip réseau (`PIP_NO_INDEX=1`), les pulls et
le réseau pendant le build de référence. Il sélectionne des wheels purs pour
Python 3.12. Le fixture testé utilise FastAPI 0.115.12, Uvicorn 0.34.2 et
Pydantic 1.10.22. Les plugins PF doivent être déjà disponibles. Le script ne
prépare ni VM, ni plugin, ni runtime, ni cache absent ; un prérequis manquant
est un échec explicite. Ne pas exécuter les scripts avec Python `-O`.

Chaque copie isolée exécute gel, build, vérification OCI, lancement détaché,
HTTP 200 avec corps attendu, inspection, arrêt et second lancement. Les sept
cas négatifs couvrent cache absent, dépendance introuvable, plan/lock obsolète,
source modifiée, manifeste altéré, port occupé et interruption réelle du gel.
Le nettoyage cible seulement les noms uniques créés par l'essai ; les
conteneurs préexistants sont inventoriés avant et après. Les fixtures d'entrée
et les rapports sont conservés. Seul Podman a été exercé ici ; la branche Docker
n'est pas qualifiée par ces résultats.

### Résultats et limites

Dix répétitions de packaging réussissent, avec digests identiques et dix
manifestes altérés refusés (`binary-run-02`). La série `fastapi-run-03` contient
dix parcours réussis, chacun relancé, sept pannes attendues correctement traitées
et trois constructions conventionnelles réussies. Aucun conteneur possédé par
ces essais ne subsiste ; les conteneurs préexistants sont conservés.

Les médianes observées sont **3,75 s / 57,8 Mo** pour PF contre **0,51 s /
131,5 Mo** pour le build Podman conventionnel. Les deux utilisent les mêmes
sources et dépendances pures, une cible Linux/amd64 et des restrictions
comparables au lancement sur la même machine macOS/arm64. La référence garde
une base Debian complète ; PF sélectionne le runtime et vérifie ses couches OCI.
Les chemins hôte/VM et les garanties diffèrent. Ces mesures avec caches préparés
n'établissent ni gain de temps global, ni supériorité générale, ni adoption.

Les premières tentatives sont conservées sous `.cache/local-validation/` :
`fastapi-run-01` échoue sur une configuration Python du lanceur ; `02` et `03`
terminent leurs parcours mais renvoient un échec global en classant à tort
l'absence d'images jamais importées comme une erreur de nettoyage ; `04` échoue
avant le premier essai, le bac à sable empêchant l'accès à la VM locale.
Après correction du diagnostic, `05` réussit un parcours relancé, les sept
pannes et une référence, avec nettoyage sans erreur. Cette vérification courte
ne remplace pas les dix répétitions de `03`.

Les journaux locaux ne sont pas distribués avec le dépôt. Ils identifient les
exécutables par hash, sans rattachement automatique à une révision. La recette
permet de produire de nouveaux résultats sur d'autres machines ; elle ne ferme
pas les essais d'installation froide, d'extensions natives, d'autres plateformes
ou de nouveaux utilisateurs. La reconstruction automatique d'une image périmée construit désormais une
remplaçante avant de déplacer l'ancienne sortie. Une publication échouée tente
de restaurer celle-ci ; si cette restauration échoue elle-même, le répertoire
de sauvegarde est conservé et son chemin affiché. La sortie CLI de réussite
n'est émise qu'après publication. Ce contrat protège l'image après échec normal
ou annulation ; il ne garantit pas une transaction globale des rapports de
release, une lecture simultanée sans interruption, ni une reprise automatique
après SIGKILL ou panne électrique entre les renommages.

La série `fastapi-run-06` ajoute un huitième cas : un obstacle à l'écriture des
preuves provoque un échec après packaging. L'index précédent reste identique,
son layout passe la vérification, puis la suppression de l'obstacle permet
une reconstruction, un index différent et une réponse HTTP correcte. Les
tests Go couvrent également l'échec de compilation, l'annulation et le nettoyage
des répertoires de remplacement.

Après les corrections, `go test -race ./...` et `go vet ./...` passent sur le
module racine ; la suite TUI passe également sous détecteur de courses. Le
transport invité passe vingt répétitions avec `-race`. Les contrôles utilisent
Go 1.26.10 déjà en cache et les téléchargements de modules désactivés. Une
première relance a été bloquée par les permissions du cache Go ; les journaux
de cette tentative sont conservés, puis les deux commandes complètes ont été
réexécutées avec succès (`verified-status.json`, codes de sortie 0 et 0).

## Recette utilisateurs à exécuter

Cette procédure prépare les cases « simplicité », « utilité » et « TUI » de la
[todo](../todo-next.md). Elle n'est pas un compte rendu de participants : aucun
nouvel utilisateur n'a encore réalisé ces essais.

Pour chacun des trois participants, partir d'un dossier vide choisi par lui et
lui donner uniquement le guide de la démo et son objectif : obtenir le Hello
World FastAPI, vérifier HTTP 200, retrouver le conteneur, l'arrêter puis le
relancer. Lui faire réaliser aussi sa procédure habituelle avec les mêmes
sources et dépendances. Alterner l'ordre PF/référence entre participants pour
réduire l'effet d'apprentissage ; noter la différence de garanties au lieu de
forcer une comparaison de fonctions différentes.

Conserver les observations dans un dossier de résultats choisi hors du projet
applicatif. Aucun enregistrement ou contact avec une personne n'est déclenché
par les scripts. La fiche suivante peut être copiée dans les notes du testeur :

| Mesure | PF | Procédure habituelle |
| --- | --- | --- |
| OS/architecture, versions, matériel | À renseigner | À renseigner |
| Outils déjà installés et état initial des caches | À renseigner | À renseigner |
| Début/fin de préparation, téléchargements nécessaires | À renseigner | À renseigner |
| Début du parcours → premier HTTP 200 | À renseigner | À renseigner |
| Commandes saisies, décisions, consultations du guide | À renseigner | À renseigner |
| Erreurs exactes, reprises et aides reçues | À renseigner | À renseigner |
| Fichiers/dossiers ajoutés, conteneurs/images restants | À renseigner | À renseigner |
| Arrêt/relance, source modifiée puis reconstruction | À renseigner | À renseigner |
| Port occupé, annulation, reprise sans perte | À renseigner | À renseigner |
| Étape que le participant remplacerait et raison | À renseigner | À renseigner |
| Tâche suivante et réutilisation effectivement observée | À renseigner | À renseigner |

Ne pas dicter les commandes. En cas de blocage, noter d'abord l'action tentée,
le message et l'attente du participant ; consigner toute intervention. Refaire
le cas après correction. Un cache préparé est une condition séparée : ne pas
présenter le temps d'un tel essai comme une installation depuis zéro.

Pour la TUI, répéter le parcours au clavier dans un terminal normal puis réduit
(par exemple 60 × 20), avec et sans couleur (`NO_COLOR=1`). Vérifier que le choix
courant et les erreurs restent compréhensibles sans couleur, que les champs et
l'aide restent accessibles, et qu'Esc/Ctrl+C annule sans confirmer. Tester une
référence vide/invalide puis sa correction, une annulation pendant une commande
active et la reprise. Comparer les choix affichés à la commande réellement
exécutée. Une réussite automatisée des modèles TUI ne remplace pas ces essais.

Les autres cases ouvertes demandent des moyens distincts :

| Chantier | Preuve ou moyen encore nécessaire |
| --- | --- |
| Installation et hyperviseurs | Machines natives des OS annoncés ; vrais cycles installation/mise à jour/retour arrière et boot/arrêt/reprise |
| KubeVirt/containerd/parcours cluster | Cluster local de test désigné, versions connues, outils et droits contrôlés ; coupures, RBAC, rollback et nettoyage |
| Huit langages et runtimes | Toolchains supportées et dépendances disponibles ; caches manquants et échecs des gestionnaires exercés, pas seulement fixtures synthétiques |
| Formats | Inventaire des versions réellement distribuées, fixtures historiques et politique de migration/retour arrière validée |
| Registry | Registre de test et fournisseurs annoncés, authentification et reprise vérifiées avec les preuves réelles |
| Multi-tenant | Plusieurs processus/nœuds en mTLS, identité des tenants, quotas disque, fairness et pertes de workers exercés |
| Sécurité et maintenance | Relecteur indépendant et responsables nommés ; résultats et corrections observables |
| Utilité et réutilisation | Participants externes et tâche suivante réellement effectuée |

Ces moyens ne sont pas remplacés par des cases cochées ni par des compilations
croisées.

## Parallels et Rosetta — essais complémentaires

Le 5 novembre 2026, les moyens locaux ont permis d'aller au-delà de la seule
compilation croisée :

| Environnement | Exécution observée | Limite |
| --- | --- | --- |
| Ubuntu 22.04 ARM64 sous Parallels, noyau Linux 6.8.0-138 | Exécutable Linux ARM64 exécuté ; CLI PF Linux ARM64 : build de projet, vérification OCI réussie, manifeste altéré refusé | Aucun moteur Docker/Podman trouvé dans cet invité ; installation/mise à jour, conteneur et backend KVM non exercés |
| macOS x86-64 sous Rosetta sur hôte ARM64 | Démarrage du CLI x86-64, construction du même type de projet et vérification OCI réussies | Traduction macOS, pas une machine Intel ni un essai Linux/Windows |
| Windows 11 ARM sous Parallels, build 22621.4317 | PF ARM64 et PF x86-64 : build OCI, vérification positive et refus du manifeste altéré réussis ; dossiers temporaires supprimés | x86-64 exécuté par l’émulation Windows ARM ; aucun conteneur, WHPX, installateur PF ou mise à jour PF exercé |
| CrossOver, bouteille de validation isolée | Initialisation bloquée ; arrêt ciblé de son serveur Wine, aucun résultat CLI obtenu | Aucun succès ni défaut PF attribué à cet essai interrompu ; la bouteille de test est supprimée, Steam reste intact |

Les exécutables ont été construits avec Go 1.26.10 en cache et les résolutions
réseau désactivées. Le test Ubuntu utilise uniquement un dossier invité
`/tmp/pf-native-*`, nettoyé en fin d'essai. Le partage temporaire des seuls
binaires est en lecture seule ; il est retiré après usage. Ubuntu est remis en
suspension comme avant l'essai. Les journaux et le SHA-256 du binaire Linux sont
conservés sous `.cache/local-validation/parallels-linux*` ; les commandes Rosetta
sous `rosetta-commands.json`. Les refus de session Windows restent dans
`parallels-windows-inventory*.log`.
Windows a retrouvé son état arrêté initial et son partage temporaire est retiré.
Le résultat CrossOver est conservé dans `crossover-smoke.json` ; il précise
l'interruption du lanceur, afin de ne pas attribuer son code de sortie à PF.

Pour reproduire le contrôle Linux avec ses propres binaires : reprendre une VM
Linux ARM64, lui donner accès en lecture seule à PF Linux ARM64 et à un petit
exécutable Linux ARM64, les copier dans un nouveau dossier temporaire invité,
puis exécuter :

```bash
./service
printf 'language: compiled\nartifact: service\nplatform: linux/arm64\nimage: local/native-validation\n' > pf.yaml
./pf project build --yes
./pf verify --format text .platform-factory/image
```

Après modification volontaire d'un blob de manifeste, `pf verify` doit retourner
un code non nul pour digest ou taille incorrecte. Supprimer uniquement le dossier
créé pour l'essai, retirer son partage et restaurer l'état initial de la VM.
Ne pas réutiliser un dossier de travail personnel pour ce cas négatif.

La virtualisation imbriquée est désactivée dans les VM inventoriées. Ces essais
ne valident donc pas un backend KVM imbriqué, HVF ou WHPX. CrossOver est une
couche de compatibilité : ses résultats doivent rester distincts de ceux d'un
vrai système Windows. Les versions des outils invités et l'architecture doivent
figurer dans toute recette supplémentaire avant de fermer les cases de support.

### Réinstallation des Tools et recette Windows

Les Tools ARM 27.1.0.58756 ont été réinstallés depuis l'ISO livré avec Parallels,
sans téléchargement. L'installateur invité a confirmé la réussite, puis Windows
a redémarré. Le service Tools est actif, mais `prlctl exec` et l'accès au partage
hôte restent indisponibles : la réparation de cette intégration n'est pas déclarée
complète. La pause automatique à l'inactivité a été temporairement désactivée
pour les commandes console, puis rétablie.

Les tests ont donc été lancés depuis un ISO local en lecture seule, contenant
uniquement les deux exécutables PF, le binaire Linux de fixture et la recette
PowerShell. La même recette est conservée dans
`scripts/validation/windows-packaging.ps1`. La placer à côté de
`pf-windows-arm64.exe`, `pf-windows-amd64.exe` et `service-linux-arm64` dans un
support dédié, puis l'exécuter dans une console classique, par exemple :

```powershell
pwsh -ep bypass -f D:\windows-packaging.ps1
```

L'option de politique est limitée à ce processus. Le script copie chaque CLI et
la fixture dans son propre dossier temporaire, construit le projet, vérifie
l'image intacte, altère un manifeste puis exige un refus. Il supprime ses
projets temporaires et conserve le JSON détaillé dans
`$env:TEMP\pf-local-native-result.json`. Lire les champs `passed`, `exit_code`
et `temporary_directory_removed` ; la sortie du script seule ne constitue pas
un code de réussite global exploitable par une CI.

La capture `windows-native-pwsh-result.png` montre `passed=True` et `cleanup=True`
pour ARM64 et amd64, la vérification intacte à code 0 et le refus du manifeste
altéré à code 1. Le compte rendu hôte `parallels-windows-observed.json` précise
que les résultats ont été relevés sur cette console ; le JSON complet reste
dans le dossier temporaire de l'invité, il n'a pas été exporté via l'API en panne.
Les tentatives de saisie et de lancement antérieures restent conservées, sans
être attribuées à un défaut PF. Aucun autre essai Windows n'est implicitement
validé par ce constat.

Après la recette, le partage temporaire est retiré, le lecteur optique retrouve
son image antérieure déconnectée, la pause à l'inactivité est réactivée et
Windows retrouve son état arrêté initial. L'ISO des Tools monté en lecture
seule sur le Mac est également démonté. Les mises à jour effectivement
installées des Tools restent en place conformément à la réparation demandée.

## Formulaires TUI et mesures — 10 novembre 2026

Les formulaires de confirmation du build et de choix du runtime réagissent
maintenant aux changements de largeur du terminal. Le cadre et les champs
s'ajustent ; les textes longs reviennent à la ligne. Le champ actif du build
porte `> Image` ou `> Tag`, pour rester identifiable sans couleur. Avant la
correction, les nouveaux tests reproduisaient un cadre dépassant 40 colonnes.
Ils passent après correction, avec la suite TUI complète sous détecteur de
courses et `NO_COLOR=1`, le vet TUI et les tests CLI/architecture du module racine.

La recette POSIX versionnée utilise un vrai pseudo-terminal 40 × 20 :

```bash
python3 scripts/validation/tui-project.py \
  --pf /chemin/pf --binary /chemin/executable-linux-arm64 \
  --results /tmp/pf-tui-resultats
```

Elle ne télécharge rien. La fixture compilée évite d'installer un gestionnaire
de dépendances. Les trois cas réellement exercés sont : Tab puis Esc ; Tab puis
Ctrl+C ; référence effacée et refusée, puis saisie correcte et image OCI vérifiée.
Les annulations retournent un code non nul sans écrire `.platform-factory/`.
Les sources et la configuration conservent leur hash. Les dossiers propres à
l'essai sont supprimés et les captures terminal brutes restent dans les résultats.
La série locale `tui-pty-01` passe les trois cas, sans séquence SGR de couleur.
Ces tests ne remplacent pas une recette humaine du marketplace, des lecteurs
d'écran, d'autres OS ou des terminaux de très faible hauteur.

Le lanceur FastAPI enregistre désormais la durée de copie de la fixture, celle
du gel jusqu'au premier HTTP 200, les commandes de projet et le nombre de
fichiers créés. La durée d'installation des outils est `null` : elle est inconnue,
puisqu'ils étaient déjà installés. Le compteur de confirmations automatiques
ne mesure pas les décisions ni les difficultés d'une personne.

La série `fastapi-run-08`, sur le CLI incluant cette correction, donne :

| Mesure locale préparée | Résultat |
| --- | --- |
| Copie de la fixture | 0,4471 s |
| Gel → premier HTTP 200 | 19,8705 s |
| Commandes PF, deuxième lancement compris | 5 |
| Fichiers nouveaux dans l'état interne | 593 |
| Nouveaux fichiers à la racine applicative | 0 |
| Parcours nominal puis relance | Réussis |
| Pannes contrôlées | 8 sur 8 |
| Référence Podman | 1 sur 1 |
| Nettoyage et conteneurs préexistants conservés | Vérifiés |

Ces nombres décrivent une seule répétition supplémentaire, avec un runtime
et un wheelhouse préparés ; ce ne sont pas des médianes ni une installation
froide. La tentative `fastapi-run-07` n'a pas atteint le premier parcours :
son ancienne fixture `/tmp` avait disparu. Une copie d'une répétition locale
conservée a servi à `08`, sans téléchargement. Le lanceur vérifie maintenant
les fichiers requis avant de créer les résultats ; une fixture incomplète
retourne une erreur de prérequis lisible, sans dossier de résultats vide.

### Prévalidation des contraintes du worker — 10 novembre 2026

Un pipeline comprenant un stage initial valide puis un stage aux contraintes
non prises en charge pouvait télécharger ses entrées et exécuter le premier
stage avant son refus. Le worker contrôle désormais tous les stages avant ces
effets de bord, en partageant la validation avec l'exécuteur local. Les cas
CPU, PIDs, secrets et politiques de sandbox reproduisent le défaut ; le réseau
non pris en charge est aussi testé sur un stage racine valide dans le DAG.
Les journaux locaux conservent le premier échec et la validation corrigée.

Cette prévalidation ne constitue pas un confinement des processus. En particulier,
`network: none` reste une déclaration acceptée par compatibilité sans blocage du
réseau hôte dans l'exécuteur simple. Les limites et les besoins d'isolation externe
restent documentés dans [les builds distribués](distributed-builds.md).

Validation de cette reprise : `go test -race ./...` et `go vet ./...` réussis
sur le module racine avec Go 1.26.10, téléchargements désactivés. Le module TUI
séparé passe également sa suite avec `-race`, `NO_COLOR=1`, et `go vet`.
Les journaux locaux sont conservés sous `.cache/local-validation/` ; ils ne
constituent pas une exécution CI ni une revue indépendante.
