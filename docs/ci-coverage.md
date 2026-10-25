# Couverture et preuves d’intégration

La CI conserve le seuil **87 %** pour la couverture unitaire. Elle publie aussi
le profil brut `coverage.out` et le rapport `coverage-all-paths.txt` : les chemins
exclus du seul pourcentage unitaire restent donc visibles, même avec zéro compteur.

Le périmètre existant écartait déjà les helpers `exec` de l’exécuteur et le boot
KVM de bas niveau. `scripts/ci/unit-coverage.go` complète cette séparation :

- les fonctions `main`, dont le PID 1 du guest, sont vérifiées par les parcours
  exécutables ; le filtre AST conserve toutes les autres fonctions du fichier ;
- console, identité, périphérique série, DHCP et arrêt du guest Linux nécessitent
  un guest démarré, vérifié dans `microvm-boot` ;
- l’orchestration native Linux et les ioctls TAP nécessitent KVM et/ou
  CAP_NET_ADMIN ; les tests natifs et les contrats de validation restent exécutés ;
- les helpers du sandbox de plugins se terminent par `syscall.Exec`, qui remplace
  le runtime Go avant l’écriture des compteurs. Les tests de sandbox restent
  exécutés ; la sélection des permissions dans le même fichier reste comptée.

Le filtre ne transforme aucun zéro en succès. Il échoue si une source du profil
est inconnue ou illisible, et ses tests vérifient la conservation de la logique
applicative et des décisions de permissions. Les tests de contrat rootfs corrigés
vérifient désormais l’erreur sémantique attendue, plutôt qu’un blob manquant.

`cmd/tui` est mesuré séparément puis agrégé au module racine. L’installateur fait
partie du module racine : son ancien second profil était redondant. Les modules
`plugins/*` ont leurs propres tests ; leur couverture n’est pas présentée comme
acquise dans ce pourcentage. Une mesure locale complète a révélé des lacunes dans
plusieurs modules de langage ; elles restent une dette distincte de ce correctif.

Une CI verte exige aussi les workflows de sécurité, runtime, OCI, sandbox et
MicroVM applicables. Le pourcentage unitaire seul ne constitue pas une validation
commerciale ni une preuve de tous les comportements possibles.
