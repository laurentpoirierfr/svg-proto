# Plan d'attaque

Stratégie en 7 phases. Règle directrice : **aucun algorithme avant l'instrument
de mesure**. L'ordre n'est pas négociable, parce que la phase 0 est ce qui
transforme « j'ai un peu amélioré » en « j'ai amélioré de X % sur le corpus, et
voilà le coût ».

## Décisions arrêtées

| # | Décision | Choix |
|---|---|---|
| D1 | **Conteneur vidéo** | SVG (poster + keyframes) + **sidecar JSON** + player minimal. Le « un seul fichier » viendra plus tard, comme étape de packaging sur la même structure interne. |
| D2 | **Cible v1** | Contenu **flat / UI / logos**. Le tracé de contours est donc le premier étage à écrire ; screenshot et dessin au trait suivent naturellement en Phase 3. |
| D3 | **Photos** | **Posterisation pilotée par le budget**, pas refus. Voir ci-dessous. |
| D4 | **Cœur de la lib** | **Go pur, zéro cgo.** `image/*` + `golang.org/x/image` uniquement. L'oracle de rendu est un CLI externe derrière un build tag, jamais une dépendance du cœur. |
| D5 | **Représentation interne** | Structs Go d'un bout à l'autre, **aucune chaîne SVG avant l'étage d'émission**. C'est ce qui permet les tests aller-retour, un second backend, et de changer d'émetteur sans toucher à l'algorithme. |
| D6 | **Ce qui n'est pas un objectif** | **La taille en octets.** Le transport est le travail de gzip/brotli, traité tard et à l'empaquetage. La contrainte dure est le **nombre de nœuds/segments**, parce qu'elle ne se comprime pas : un encodeur naïf produit 10³–10⁵ nœuds par image, donc 10⁵–10⁷ par seconde à 24 ips. Voir `docs/phase1.md` pour la mesure. |

> `internal/dom` et `internal/baseline` implémentent D5 et D6 : le premier est
> l'arbre de structs Go sans aucun `encoding/xml`, le second applique un budget
> de nœuds via `-max-nodes`.

### D3 en détail : la posterisation est un problème de budget, pas de goût

La tentation, en faced à une photo, est de produire un escalier de
micro-contours qui détruit l'image tout en restant plus gros que le PNG. Ce qui
lui manque, c'est un **point d'opération** : relâcher les leviers jusqu'à tenir
dans un budget de nœuds, et s'arrêter là.

Leviers, dans l'ordre où on les relâche :

1. réduire `k` (la palette) — de loin le plus fort sur une photo
2. augmenter la tolérance de simplification
3. **aplatir les régions trop bruitées** — le seuil se décide sur la densité de
   contours *locale*, pas globalement
4. décimer la résolution du `viewBox`
5. en dernier recours, une couche raster pour le résidu

La sortie porte toujours un **diagnostic** : `k` retenu, régions dégradées, score
de fidélité, ratio contre le PNG. L'appelant peut alors décider, et la CI peut
échouer. C'est la différence entre un mode honnête et une cascade silencieuse.

Ce qui reste inacceptable : un mode photo dont le résultat n'est **ni plus petit
ni plus proche** du source. Les criteria restent le score composite et le ratio
(voir la Definition of Done).

---

## Phase 0 — Instrumentation (la phase la plus importante)

**Pourquoi d'abord.** Sans mesure, tout le reste est de l'intuition. Et parce
que la baseline (`<image href=png>`) doit être mesurée avant le premier
algorithme, sinon on se compare au mauvais référentiel.

### Livrables

1. **Corpus de test** — * amorcé : 7 cas dans `testdata/corpus/` ; reste à étendre (~30–60 images, avec licence
   et attribution dans un `manifest.yaml`) :
   - logos / icônes (flat, 2–4 couleurs)
   - captures d'écran (UI, beaucoup d'aplats, du texte)
   - dessin au trait / croquis
   - pixel art
   - photo (3–5, pour calibrer la posterisation pilotée par le budget)
   - photo avec alpha (test du bug de halo)
   - scène d'animation : 24 frames cohérentes, pour la phase vidéo

2. **`cmd/svgstat`** — la mesure :
   - octets (bruts, gzip, brotli)
   - nombre d'éléments par type, nombre total de nœuds
   - nombre de commandes de path par type, buckets de précision décimale
   - nb de `fill` distincts, profondeur d'arbre
   - `viewBox`, `width`/`height`, `font-family` (portabilité)
   - mode `--diff a.svg b.svg` : delta de toutes les métriques
   - mode `--render-in=WxH --ref=source.png` : rastérise via l'oracle externe
     et calcule PSNR / ΔE OKLab / **SSIM**
   - **JSON en sortie** pour l'agrégation dans la CI

3. **Oracle de rendu** : `resvg` (qualité de référence) ou `inkscape` (présent
   dans l'environnement) via un CLI externe, derrière un build tag
   `//go:build raster_oracle`, pour que le cœur de la lib reste sans
   dépendance et sans cgo.

4. **Score composite** : un seul chiffre lisible, plus le détail.
   - dominant : MSE en **OKLab**, **pondéré par la magnitude du gradient**
   - rapportés : ΔE OKLab moyen, PSNR, octets, nœuds
   - affiché comme **qualité + coût**, jamais un score seul

5. **Score d'atteignabilité** : `max(0, 1 − taille_SVG / taille_png)`
   → un SVG plus gros que l'original est un échec, quel que soit le SSIM.

### Critère de sortie
`svgstat` tourne sur le corpus complet et produit un rapport de référence
versionné dans le dépôt. On sait exactement où on part.

---

## Phase 1 — Les baselines honnêtes

Objectif : publier la null hypothesis et le « meilleur des choses bêtes »,
pour que tout le reste soit mesuré contre quelque chose de réel.

1. **Baseline A** : PNG/JPEG → `<image href="data:...">`.
2. **Baseline B** : quantification + grille de rectangles (§3.B) — le « on peut
   faire ça en 200 lignes » qui plafonne.
3. **Baseline C** : quantification + run-length horizontal (§3.C).

### Livrables
Un tableau dans `docs/benchmarks.md` : taille, nœuds, SSIM par mode et par
catégorie de contenu. Et une conclusion honnête : sur quelles catégories le
vecteur ne peut **pas** gagner. Cette table est un livrable public du projet.

### Critère de sortie
On sait dire, avec des chiffres, jusqu'où le vecteur gagne sur une photo, et à
quel point du budget il cesse d'y gagner. Cet équilibre est le point d'opération
du mode photo (D3), et il est mesuré, pas estimé.

---

## Phase 2 — Le traceur de contours (le cœur, v1.0)

Implémentation de la stratégie D du §3 de la réflexion, étage par étage.

### Ordre d'implémentation (chacun testable seul)

1. `internal/pixelbuf` — décodage, buffer planaire, sRGB → linéaire → OKLab,
   gestion de l'alpha (prémultipliée correcte, cf. §5.1 de la réflexion)
2. `internal/quant` — median-cut **en OKLab**, `k` paramétrable, **sans tramage**
3. `internal/field` — lissage anisotrope (Perona-Malik), gradient, magnitude
4. `internal/contour` — marching squares sous-pixel + **décision asymptotique**,
   chaînage de boucles, imbrication des trous, orientation normalisée, nettoyage
5. `internal/simplify` — Douglas-Peucker à **tolérance pondérée par le gradient**,
   suppression des colinéaires
6. `internal/polygon` + `internal/curvefit` — coins réels contre artefacts de raster,
   puis ajustement de Béziers (Schneider). Le paquet existe et est testé ; il est
   désactivé tant que `polygon` ne remplace pas la marche d'escalier en entrée
7. `internal/pathdata` — construction et **minimisation** des `d` (abs/rel au
   caractère près, quantification relative, `h`/`v`/`s`/`t`)
8. `internal/dom` — l'arbre SVG interne (aucune chaîne avant la fin !)
9. `internal/encode` — sérialisation + grouping par couleur

### Contrat de chaque étage
```go
func (p Params) Run(in T) (T, error)   // fonction pure, T = struct plain
```
- entrée/sortie = **structs Go**, jamais du SVG
- chaque étage a ses benchmarks et ses goldens
- **déterminisme** : même entrée → octets identiques

### Critère de sortie
Sur les catégories *flat* du corpus : qualité perçue au niveau d'`potrace`,
`d` plus court que `svgo` **à erreur de rendu équivalente**, et un rapport
`svgstat` complet. Le tout sans aucune dépendance externe dans `go.mod`.

---

## Phase 3 — Les spécialisations (petit effort, gros gain)

Chacune est un mode de la même lib, pas un programme séparé.

1. **Mode pixel art** : sans lissage, palette dure, arêtes franches,
   `shape-rendering="crispEdges"`. Fidélité exacte.
2. **Mode screenshot / UI** : décomposition en rectangles maximaux (greedy
   meshing) + **détection de texte → vrai `<text>`**. C'est le mode qui a le
   meilleur rapport effort/valeur : le texte réel est sélectionnable,
   accessible, et 10× plus léger que des contours de glyphes.
3. **Mode dessin au trait** : squelettisation (thinning) + tracé de graphe →
   `fill="none" stroke="..."` avec `stroke-linecap`/`linejoin`. Sans ça, un
   croquis devient un anneau de contours : mauvais *et* 2× plus lourd.
4. **Mode dégradés** : détection de ramps linéaires → `<linearGradient>`.
   Petit corpus, gros gain sur les logos de marque.
5. **Mode photo (posterisation pilotée par le budget)** : itère sur
   `k` → tolérance → aplatissement local → `viewBox` → calque raster, et
   s'arrête quand le budget de nœuds est atteint. Émet toujours un diagnostic
   structuré (`k`, régions dégradées, score, ratio). Voir D3.

### Critère de sortie
Chaque mode a un seuil de déclenchement automatique explicite, et une page
de démo. Le mode par défaut choisit le bon mode.

---

## Phase 4 — Sémantique, métadonnées, accessibilité

C'est là que se crée la **différenciation** — personne ne le fait, et c'est
gratuit une fois la Phase 2 faite.

1. **Groupement par rôle** : fond / encre / accent, par composante connexe.
2. **Accessibilité** : `<title>`, `<desc>`, `role="img"`, `aria-label` par
   groupe, et pourquoi pas un `<text>` réel à côté.
3. **Identifiants stables** entre deux runs (hash de la géométrie ou naming
   scheme déterministe) → **les diffs VCS deviennent lisibles**. Un SVG généré
   qui produit des diffs incompréhensibles est inutilisable en revue de code.
4. **Métadonnées** : profil (mode, `k`, tolérance), SVG original, hash, outils
   utilisé → dans un `<metadata>` ou un sidecar.

### Critère de sortie
Le SVG produit est exploitable par un lecteur d'écran, et `git diff` sur un
asset généré est lisible.

---

## Phase 5 — Vidéo (la cible réelle)

### 5.0 Décodage
Interface `FrameSource` découplée. Deux implémentations :
- `ffmpeg` via un **processus externe** feeding un pipe `rawvideo` (pragmatique,
  rapide, déjà disponible)
- `mediacommon` (+ codecs Go) si on veut du pur Go — plus lent, à garder en
  option. Ne pas bloquer le projet dessus.

### 5.1 Tracking inter-frames
Descripteurs par contour (boîte englobante, centroïde, aire, moments) →
matrice de coût (IoU + distance centroïdes + variation d'aire) →
**assignation hongroienne** → suivi d'identité stable.
C'est la brique qui rend le delta-encoding possible.

### 5.2 Delta-encoding / keyframes
On ne réémet que ce qui change : formes apparues, disparues, modifiées.
Le budget nœuds force l'insertion de keyframes.

### 5.3 Animer par transformation — **le gros levier**
Forme suivie d'un mouvement affine = 1 `<path>` statique + `<animateTransform>`.
Regrouper les trajectoires en lanes (translation / rotation / échelle) plutôt
qu'en keyframes. Zéro re-traçage. Gain attendu : **1 à 2 ordres de grandeur**
sur du contenu 2D.

### 5.4 Budget & dégradation
`internal/budget` : contrat API, pas un réglage. `MaxNodes` en entrée, garantie
en sortie. Le budget porte sur les **nœuds**, jamais sur les octets (D6).
Échelle de dégradation, dans cet ordre :
1. simplifier davantage (ε plus grand)
2. baisser `k` (palette)
3. décimer le framerate (« on twos » → 12 fps)
4. **basculer une région en `<image>` embarqué** (le filet de sécurité)
Chaque seuil est un budget chiffré, et `svgstat` le vérifie.

### 5.5 Vectorisation espace-temps (le lever recherche)
C'est le seul endroit du projet qui est de la recherche, et c'est ce qui
distingue fondamentalement le projet d'un encodeur d'images par lots.
- extraire les caractéristiques d'arête dans le **volume espace-temps**
- les fiter comme courbes 3D
- une caractéristique = **un seul chemin animé** pour toute sa durée de vie
Complexité : O(trajectoires) au lieu de O(frames × formes).
À traiter **après** 5.1–5.4 qui sont déjà un produit.

### 5.6 Conteneur — **décision à trancher avant d'écrire le code**
Elle détermine toute la structure de données de la phase 5.

| Option | Pour | Contre |
|---|---|---|
| **A. SVG animé autonome** (SMIL / WAAPI inliné) | un seul fichier, beau conditionnement | SMIL en voie d'abandon côté outillage ; WAAPI ne vit pas dans un `.svg` brut ; runtime non garanti |
| **B. SVG (poster + keyframes) + sidecar JSON** + player minimal | **90 % du bénéfice, 10 % de la complexité**, standard du web, débogable, re-encodable | deux fichiers, il faut un player |

→ **Recommandation : B d'abord**, A comme export de packaging.
Structure cible : `svgvid` émet `poster.svg` + `timeline.json`
(pistes, IDs de formes, transformations, timing, nœuds de fallback raster).

### 5.7 Critère de sortie
Une animation 2D de 3 s, 12–24 fps, tient dans un budget nœuds convenu,
reste à **60 fps de lecture** dans Chrome/Firefox, et `svgstat` le prouve.
Le player fait du scrubbing fluide.

---

## Phase 6 — Outillage & diffusion

- `cmd/svgimg` — image → svg (CLI, flags par mode)
- `cmd/svgstat` — la mesure (dès la Phase 0)
- `cmd/svgopt` — optimisation de `d` seule
- `cmd/svgvid` — vidéo → svg + sidecar
- `cmd/svgreplay` — preview / scrub / comparaison
- **WASM** : la même lib compilée, pour conversion dans le navigateur
- **CI** : goldens, fuzz, « ce PR n'augmente pas la taille des assets »,
  rapport `svgstat` + renders avant/après attachés au PR
- **Docs** : matrice de capacités honnête (ce que ça fait, ce que ça ne fait
  pas), page de benchmarks avec les échecs inclus

---

## Jalons

| # | Jalon | Sortie |
|---|---|---|
| M0 | Instrumentation | `svgstat` + rapport de référence versionné |
| M1 | Baselines | tableau de comparaison, domaine viable défini |
| M2 | Traceur v1 | qualité `potrace`, `d` ≤ `svgo`, zéro dépendance |
| M3 | Modes spécialisés | pixel art, screenshot+texte, trait, photo/refus |
| M4 | Sémantique | a11y, IDs stables, diffs VCS lisibles |
| M5 | Vidéo v1 | 3 s d'animation 2D sous budget, 60 fps |
| M6 | Espace-temps | trajectoires, O(features) |
| M7 | Outillage complet | CLIs, WASM, CI, docs |

## Points de décision (à trancher explicitement, pas par défaut)

1. **Conteneur vidéo : A ou B** → bloque toute la Phase 5
2. **Pure Go obligatoire pour le cœur ?** → oui recommandé (sinon personne ne
   peut builder la lib) ; cgo toléré uniquement dans l'oracle de test
3. **Level-set (marching squares) vs. contour binaire** → level-set, pour la
   qualité sous-pixel ; mais ça coûte un étage de plus
4. **Quadratique (Selinger) ou cubique (Schneider) par défaut** → quadratique,
   avec le cubique en fallback ; à confirmer par la mesure
5. **Emplacement du module** : `github.com/<handle>/svg-proto` — à fixer
6. **Portabilité des textes** : `<text>` (léger, éditable, non portable) ou
   outlines (portable, non éditable) — les deux, via un niveau de conformité

## Definition of Done (le test unique, à appliquer à toute PR)

> Sur le corpus complet :
>
> 1. **fidélité** — le score composite pondéré par le gradient est meilleur que
>    `n-1`, et **aucune régression** sur une catégorie du corpus ;
> 2. **nœuds** — sous le `MaxNodes` déclaré, et inférieur ou égal à `n-1` ;
> 3. **éditabilité** — le nombre de formes distinctes (et non de nœuds) est
>    supérieur, les groupes sont nommés, et le document reste déterministe.
>
> La taille en octets n'en fait **pas** partie (D6).

Pas de « ça a l'air mieux sur cette image ».
