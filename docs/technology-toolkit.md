# Analyseurs de politiques hors ligne

Le toolkit fournit des analyseurs JSON locaux pour vérifier des politiques
et des contrats d'entrée. Ils produisent des constats et des corrections
attendues, sans vérifier la configuration réelle d'une infrastructure distante.
Utiliser `pf extensions list` et `pf extensions inspect NAME` pour découvrir
les règles effectivement disponibles.

## Utilisation immédiate

```sh
pf extensions list
pf extensions inspect rag-retrieval-bounds
printf '%s\n' '{"top_k":5,"min_score":0.7}' > retrieval.json
pf extensions analyze rag-retrieval-bounds --input retrieval.json
```

Code de sortie : `0` si tous les contrôles passent, `1` si des constats existent,
`2` pour une mauvaise invocation/entrée. Les rapports incluent règle, chemin JSON
et correction attendue, jamais la valeur détectée. L’entrée est bornée à 1 MiB,
et la sortie à 100 constats (avec `truncated: true` si la limite est atteinte). Un champ obligatoire manquant ou une collection vide
ne passe pas silencieusement. `--input -` lit stdin.

Certains plugins acceptent directement des objets standards (Deployment Kubernetes,
PDB, HPA, SPDX, provenance SLSA, manifeste OCI) ; d’autres acceptent une politique
normalisée de votre application (cache, budgets, sauvegardes, RAG). Utilisez
`inspect NAME` pour connaître **exactement** les chemins et seuils pris en charge.
Un contrôle de présence/digest déclaré ne vérifie pas les octets distants, une
signature cryptographique, un endpoint réseau ou la configuration effective d’un
service. La vérification cryptographique des bundles demeure `pf plugin verify`.

## Distribution signée

Le composant installable `toolkit` est inclus dans la sélection `all` et les
bootstraps Bash/PowerShell. Il construit le binaire partagé une seule fois.
Chaque copie signée garde son nom propre pour sélectionner son analyseur.

```sh
./install.sh --components toolkit --yes --prefix "$PWD/dist/bin"
# Ou, depuis les sources :
go build -trimpath -o dist/platform-factory-toolkit ./cmd/platform-factory-toolkit
pf extensions bundle --executable dist/platform-factory-toolkit \
  --output dist/toolkit-plugins --key-dir /absolute/private/publisher
pf plugin verify --from dist/toolkit-plugins/supply-sbom-spdx \
  --key dist/toolkit-plugins/supply-sbom-spdx/publisher.pem
pf plugin install --from dist/toolkit-plugins/supply-sbom-spdx \
  --key /absolute/trusted-publisher.pem
```

L’éditeur conserve les clés privées hors distribution. Le destinataire doit
vérifier la clé publique par un canal de confiance ; une clé livrée avec un bundle
n’est pas automatiquement digne de confiance. Les sorties existantes ne sont pas
écrasées. Les 50 répertoires `plugins/<nom>/plugin.json` sont des templates de source
avec digest nul ; `extensions bundle` produit les vrais digests et signatures.
Les exécutables sont natifs pour la cible choisie : reconstruisez le toolkit pour
chaque OS/architecture de distribution. Les plugins ne sont pas publiés fictivement
sur le marketplace : publier des tags/versionnements externes reste une opération
séparée.

## Interface de contrôle

Le serveur MCP expose `pf_extension_catalog` et `pf_extension_check`.
Avec `name` seul, le contrôle renvoie le contrat et un exemple ; avec
`document`, il exécute la vérification. Les résultats sont bornés et les erreurs
restent distinctes d'une validation réussie.
