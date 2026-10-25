# Exemple reproductible : incident → correctif validé

Ce test utilise un dépôt temporaire et un correcteur déterministe, sans modèle,
sans réseau et sans PR distante. L’intégration d’un correcteur IA suit le même
contrat stdin/cwd, avec son propre sandbox.

```sh
python3 -m unittest discover -s internal/hooks -p 'test_*.py' -v
```

La suite prouve qu’une erreur reproductible peut produire un commit sur une
branche `autofix/*`, avec auteur/committer explicites, tout en préservant les
fichiers non commités du checkout initial. Le test de publication intercepte
seulement les transports `git push` et `gh pr create`, puis vérifie branche,
mode brouillon et absence de logs dans la description.

Configuration et activation : [hooks IA et autofix](../../docs/ai-hooks-and-autofix.md).
