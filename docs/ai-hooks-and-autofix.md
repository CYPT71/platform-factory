# Hooks IA et corrections proposées en PR

Les hooks couvrent les commandes CLI et chaque appel d’outil MCP :
`command.started`, `command.succeeded`, `command.failed`, `tool.started`,
`tool.succeeded`, `tool.failed`. Ils sont désactivés tant que
`PLATFORM_FACTORY_HOOKS_CONFIG` ne pointe pas vers un fichier absolu explicite.
Un dépôt cloné ne peut donc pas activer des exécutables à votre insu.

Les callbacks reçoivent sur stdin un objet JSON versionné contenant un ID,
le nom de commande/outil, le code de sortie, le répertoire et la durée.
Les arguments, prompts, entrées d’outils et logs applicatifs ne sont pas transmis.
Les callbacks utilisent des listes argv, un timeout et un environnement minimal ;
les variables supplémentaires doivent être autorisées par nom. Leur sortie est
ignorée pour préserver stdout JSON/MCP. Une erreur de callback ne remplace pas
le code de sortie de l’application. `PLATFORM_FACTORY_HOOKS_ACTIVE=1` empêche
les appels récursifs.

## Activer un callback local

Créez hors du dépôt un fichier privé, par exemple `/absolute/config/hooks.json` :

```json
{
  "version": 1,
  "hooks": {
    "command.failed": [{
      "command": ["/absolute/bin/pf", "hooks", "autofix", "--config", "/absolute/config/autofix.json"],
      "timeout_seconds": 1800,
      "pass_env": ["HOME", "PATH"]
    }]
  }
}
```

```sh
chmod 600 /absolute/config/hooks.json
pf hooks validate --config /absolute/config/hooks.json
export PLATFORM_FACTORY_HOOKS_CONFIG=/absolute/config/hooks.json
```

Pour les outils MCP, ajoutez le même callback sous `tool.failed`. Le serveur
utilise son checkout configuré comme répertoire d’incident. Les commandes CLI
devraient être lancées à la racine du dépôt configuré pour l’autofix.

## Configurer la reproduction, le correcteur et les tests

Le runner autofix v1 fonctionne sous Linux et macOS ; il nécessite Python 3.11+,
Git et, pour publier, `gh` authentifié. Les callbacks CLI/MCP restent compilables
sous Windows, où un timeout termine le processus principal du callback.
Exemple de configuration **à adapter** à vos chemins et à votre correcteur :

```json
{
  "version": 1,
  "repository": "/absolute/project",
  "base": "main",
  "author_name": "CYPT71",
  "author_email": "cyprientaib1@gmail.com",
  "fixer": ["/absolute/bin/pf", "hooks", "codex-fixer"],
  "reproduce": ["/absolute/bin/go", "test", "./internal/affected"],
  "checks": [["/absolute/bin/go", "test", "./..."], ["/absolute/bin/go", "vet", "./..."]],
  "allowed_paths": ["internal/affected/", "cmd/platform-factory/"],
  "timeout_seconds": 300,
  "max_patch_bytes": 262144,
  "pass_env": ["PATH", "GOCACHE", "GOMODCACHE", "GOTOOLCHAIN"],
  "fixer_pass_env": ["HOME", "PATH"],
  "publish_pr": false,
  "github_repository": "CYPT71/platform-factory"
}
```

L’adaptateur inclus `pf hooks codex-fixer` utilise un Codex CLI déjà installé et
authentifié. Il lance une seule session éphémère, sans demandes d’approbation,
avec sandbox `workspace-write`, réseau des commandes, recherche web, apps, plugins
et sous-agents désactivés. Les serveurs MCP configurés sont explicitement désactivés
pour cette invocation. Il ne commite ni
ne publie lui-même. `PF_CODEX_BIN` peut fixer un exécutable absolu et
`PF_CODEX_MODEL` sélectionner un modèle, via `fixer_pass_env`. Les diagnostics sont
limités à 8192 caractères avant d’être transmis au modèle. Les options ont été
vérifiées sur le CLI local 0.164.0 ; l’intégration au fournisseur reste optionnelle
et n’est pas appelée par les tests.

Un autre correcteur peut être choisi avec le même contrat :
stdin contient l’incident et un diagnostic borné marqué comme **donnée non fiable** ;
le cwd est le worktree temporaire ; le programme doit modifier les fichiers permis
et sortir avec zéro. Aucun fournisseur, abonnement ni secret n’est supposé.
Une IA doit être lancée avec son propre sandbox, par exemple via un adaptateur
qui limite ses outils et son réseau. Un worktree Git isole les modifications du
checkout ; **ce n’est pas un sandbox système** pour un programme malveillant.

La reproduction doit échouer avant la correction. Après correction, cette même
commande et chaque contrôle obligatoire doivent réussir. Le runner refuse les
changements hors allowlist, les symlinks, les fichiers spéciaux, les changements
d’historique et les patchs dépassant 30 fichiers ou le budget d’octets. Les dossiers
`.git`, `.github`, `.agents`, `.codex` et `.aws` ne peuvent pas être autorisés.

La reproduction et le correcteur disposent par défaut d’un HOME temporaire privé,
supprimé à la fin ; aucun HOME utilisateur n’est hérité implicitement. Les caches Go
peuvent être réutilisés en autorisant explicitement GOCACHE/GOMODCACHE.

Un incident et une révision de base réservent atomiquement un seul essai dans
le Git common-dir, sous `platform-factory-autofix/`. Même après un échec, une
nouvelle livraison ne relance pas automatiquement l’IA. Le fichier `state.json`
indique l’état ; pour retenter volontairement, archivez ce répertoire d’incident.
Les diagnostics ne sont pas persistés ni copiés dans la PR. Leur masquage de secrets
est une précaution supplémentaire, pas une garantie de nettoyage de tout log :
n’utilisez pas une reproduction qui imprime des secrets, et n’autorisez pas les
credentials du fournisseur dans `pass_env`. Utilisez `fixer_pass_env` pour les
variables réservées au correcteur : elles ne sont pas transmises à la reproduction
ni aux tests. En CI, le secret optionnel `PF_AUTOFIX_PROVIDER_KEY` est disponible
sous ce même nom pour un adaptateur de fournisseur explicitement configuré.

Avec `publish_pr: false`, un commit local validé reste sur une branche `autofix/*`.
Avec `publish_pr: true`, le runner vérifie que `origin` correspond exactement au
repository GitHub configuré, pousse uniquement cette branche, puis ouvre une PR
**brouillon**, sans fusion automatique. Auteur et committer viennent des deux
champs explicites de configuration ; aucun pied de page de co-auteur n’est ajouté.
Si la création de PR échoue après le push, la branche et son état restent disponibles.
Le checkout de départ, y compris ses fichiers non commités, reste intact.

## Détecter les échecs CI sans exécuter du code de fork

Le workflow `incident-autofix` peut sonder toutes les six heures les derniers
échecs de `ci-quality`, `security-analysis`, `kind-multinode-runtime` et
`runtime-integration`, uniquement sur le HEAD de la branche par défaut et pour
un événement `push`. Il n’utilise ni `workflow_run`, ni `pull_request_target`, ni
artifact d’échec comme source de code ou de commandes.

Pour l’activer, un administrateur définit `PF_AUTOFIX_ENABLED=true` et
`PF_AUTOFIX_CONFIG` dans les variables du repository, puis lance une première
exécution manuelle. La configuration est identique à celle ci-dessus ; le workflow
fixe lui-même repository/base/target et remplace `${WORKSPACE}` dans les arguments.
Les exécutables configurés doivent être présents sur le runner. `publish_pr` doit
être explicitement activé pour créer une PR, et la politique GitHub Actions du dépôt
doit autoriser les PR via `GITHUB_TOKEN`. Les droits d’écriture sont limités au job
activé. La déduplication consulte aussi les PR ouvertes **et fermées** de la branche.
Une exécution manuelle permet un nouvel essai explicite ; la sonde planifiée ne
relance pas automatiquement un correcteur déjà essayé sur le même HEAD.

Le workflow reste désactivé dans un dépôt sans ces variables : aucun appel IA,
coût fournisseur ou changement de configuration de compte n’est déclenché par
l’installation des plugins.

## Validation

```sh
go test ./internal/hooks ./internal/mcp ./cmd/platform-factory ./cmd/platform-factory-toolkit
python3 -m unittest discover -s internal/hooks -p 'test_*.py' -v
```

Les tests exécutent de vrais dépôts/worktrees Git, vérifient les deux identités
du commit, la préservation du checkout sale, les refus de patch, les timeouts,
la déduplication et le contenu de la demande de PR. Le transport GitHub y est
simulé ; les tests n’ouvrent pas de PR de démonstration sur votre dépôt.
