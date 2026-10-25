# Cinquante plugins IA et cloud

Cette extension fournit **50 plugins d’analyse JSON hors ligne**, regroupés en
dix domaines. Ce sont des contrôles de politiques locaux, chacun avec une entrée
contractuelle, des constats exploitables et des exemples acceptés/refusés. Ce ne
sont pas des connecteurs cloud ni une preuve de conformité de votre infrastructure.
Aucun appel LLM, accès réseau, secret ou fichier hôte n’est nécessaire à leur RPC.

Le choix des domaines s’appuie sur les sources primaires consultées le 24 octobre
2026 : [roadmap MCP du 22 août 2026](https://blog.modelcontextprotocol.io/posts/mcp-roadmap/),
[Kubernetes 1.36 et ses évolutions déclaratives](https://kubernetes.io/blog/2026/04/22/kubernetes-v1-36-release/),
[conventions GenAI OpenTelemetry et leur déplacement](https://opentelemetry.io/docs/specs/semconv/gen-ai/),
[OCI Distribution](https://github.com/opencontainers/distribution-spec/blob/main/spec.md)
et [provenance SLSA](https://slsa.dev/spec/v1.2/provenance).
Les évolutions de MCP et des conventions GenAI restent versionnées séparément :
ce lot ne prétend pas migrer le transport MCP existant vers la dernière spécification.

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
pf plugin verify --from dist/toolkit-plugins/ai-token-budget \
  --key dist/toolkit-plugins/ai-token-budget/publisher.pem
pf plugin install --from dist/toolkit-plugins/ai-token-budget \
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

## Accès IA avec contexte réduit

Le serveur MCP expose seulement `pf_extension_catalog` et `pf_extension_check`.
Un appel `pf_extension_check` avec `name` seul renvoie le contrat/exemple ; avec
`document`, il lance le contrôle. Ainsi les 50 ensembles de règles ne sont pas
injectés systématiquement dans le contexte du modèle. Aucune estimation inventée
de pourcentage de tokens économisés n’est présentée.

Les [hooks IA et le parcours autofix/PR](ai-hooks-and-autofix.md) permettent
ensuite de traiter les échecs reproductibles.

## Catalogue

| Plugin | Domaine | Vérification |
| --- | --- | --- |
| `ai-cache-policy` | ai | Require tenant-scoped bounded prompt cache policies |
| `ai-output-schema` | ai | Require strict structured model output contracts |
| `ai-retry-policy` | ai | Prevent unbounded LLM retries and missing backoff |
| `ai-token-budget` | ai | Enforce input and output token budgets |
| `ai-tool-allowlist` | ai | Reject wildcard tool execution permissions |
| `data-backup-policy` | data | Require encrypted tested backups with a recovery objective |
| `data-postgres-tls` | data | Require PostgreSQL certificate hostname verification |
| `data-redis-security` | data | Require Redis TLS and named ACL users |
| `data-retention-policy` | data | Bound retention and require deletion propagation |
| `data-vector-index` | data | Validate bounded HNSW construction parameters |
| `delivery-autoscaling-bounds` | delivery | Validate HPA replica bounds and CPU utilization target |
| `delivery-gpu-requests` | delivery | Require matching bounded GPU requests and limits |
| `delivery-incident-routing` | delivery | Require deduplicated incident routing with an owner |
| `delivery-rollout-policy` | delivery | Require health-gated bounded progressive delivery |
| `delivery-sandbox-policy` | delivery | Require isolated AI execution without ambient credentials |
| `gateway-auth-policy` | gateway | Require audience validation and disabled anonymous inference |
| `gateway-backend-tls` | gateway | Require TLS verification for inference gateway backends |
| `gateway-retry-budget` | gateway | Prevent multiplicative gateway retry storms |
| `gateway-route-timeouts` | gateway | Bound inference gateway request and connection deadlines |
| `gateway-traffic-weights` | gateway | Validate canary backend weights sum to 100 |
| `k8s-disruption-budget` | kubernetes | Require a protective PodDisruptionBudget minimum |
| `k8s-health-probes` | kubernetes | Require startup and readiness probes on service containers |
| `k8s-image-digests` | kubernetes | Require digest-pinned images in Deployment containers |
| `k8s-pod-security` | kubernetes | Check restricted container security settings |
| `k8s-resource-bounds` | kubernetes | Require CPU and memory requests and limits |
| `mcp-command-pins` | mcp | Reject unpinned npx MCP package launchers |
| `mcp-readonly-hints` | mcp | Validate explicitly declared read-only tool annotations |
| `mcp-remote-tls` | mcp | Require HTTPS endpoints in remote MCP inventories |
| `mcp-tool-budget` | mcp | Bound exposed toolset size to reduce context cost |
| `mcp-tool-schema` | mcp | Require bounded object schemas for MCP tools |
| `platform-ci-permissions` | platform | Keep GitHub workflow permissions read-only by default |
| `platform-container-hardening` | platform | Check normalized container runtime hardening policies |
| `platform-secret-references` | platform | Allow secret references rather than inline secret values |
| `platform-terraform-state` | platform | Require locked encrypted remote Terraform state |
| `platform-webhook-security` | platform | Require webhook HMAC verification and replay protection |
| `rag-chunk-budget` | rag | Bound RAG chunks and overlap before ingestion |
| `rag-citation-evidence` | rag | Require source identifiers and chunk hashes in retrieved evidence |
| `rag-embedding-dimensions` | rag | Validate dimension agreement between model and index |
| `rag-retrieval-bounds` | rag | Bound retrieval top-k and minimum score |
| `rag-tenant-filter` | rag | Require tenant isolation in normalized retrieval policies |
| `supply-artifact-digests` | supply-chain | Verify OCI descriptor digest shape and nonnegative size |
| `supply-dependency-licenses` | supply-chain | Enforce an explicit dependency license allowlist |
| `supply-provenance-slsa` | supply-chain | Check SLSA provenance predicate and SHA-256 subjects |
| `supply-sbom-spdx` | supply-chain | Check SPDX document identity and package inventory |
| `supply-signature-policy` | supply-chain | Reject unsigned artifacts and unconstrained signers |
| `telemetry-genai-privacy` | telemetry | Keep prompt and response content collection explicitly disabled |
| `telemetry-genai-usage` | telemetry | Validate normalized GenAI token usage attributes |
| `telemetry-otlp-tls` | telemetry | Require encrypted verified OTLP export |
| `telemetry-sampling-budget` | telemetry | Bound head sampling and preserve error traces |
| `telemetry-slo-targets` | telemetry | Require measurable availability and latency SLOs |
