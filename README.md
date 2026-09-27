# svg-proto

Vectoriser des images (et, à terme, des vidéos) en SVG, en Go, sans dépendance
externe ni cgo.

Le dossier est en cours de cadrage : la réflexion et le plan sont posés, la
Phase 0 (l'instrument de mesure) et la Phase 1 (les baselines de référence) sont
implémentées. **Aucun algorithme de vectorisation n'existe encore** — c'est
délibéré, voir ci-dessous.

- **[docs/reflexion.md](docs/reflexion.md)** — la réflexion : ce qu'est vraiment
  le problème, le spectre des stratégies, la métrique, et pourquoi la vidéo
  change tout.
- **[docs/plan.md](docs/plan.md)** — le plan d'attaque en 7 phases, avec les
  jalons, les points de décision et la définition de « terminé ».
- **[docs/phase1.md](docs/phase1.md)** — l'analyse des baselines, et pourquoi la
  contrainte est le nombre de nœuds.
- **[docs/benchmarks.md](docs/benchmarks.md)** — le tableau mesuré, régénéré par
  `make bench-baselines`.

## La thèse, en une phrase

Transformer une image binaire en SVG n'est pas une conversion de format, c'est
un problème de **vectorisation** : la perte est inévitable, et la seule question
est *quelle erreur on minimise et pour quel usage*.

La valeur n'est donc pas la fidélité, c'est **l'éditabilité** — reconnaître 40
formes au lieu de 65 536 pixels, et les rendre recolorables, re-stylables,
animables, accessibles et diffables en VCS. On vise une **fidélité stylisée et
bornée**, pas la photoréalité.

Corollaire non négociable : la seule baseline qui compte n'est pas « les autres
vectoriseurs », c'est

```xml
<svg viewBox="0 0 W H"><image href="data:image/png;base64,..."/></svg>
```

qui est déjà du SVG valide, sans perte, à ~1.0× la taille du bitmap. Tout ce
qu'on écrit doit battre ce chiffre sur un axe — nombre de nœuds, fidélité,
accessibilité — jamais « en moyenne ». Et comme les octets ne sont pas un
objectif (D6), le seul seuil qui compte est : **à budget de nœuds égal, le
traceur bat-il l'encapsulation ?**

## Pourquoi la Phase 0 avant tout le reste

Parce que sans mesure on ne peut pas distinguer une amélioration d'un
changement d'apparence, et qu'on risque d'optimiser la mauvaise chose (la taille
seule produit du SVG illisible et sans intérêt). C'est pour ça que `svgstat`
arrive avant le premier traceur de contour.

## L'instrument : `svgstat`

```console
$ svgstat logo.svg
$ svgstat logo.svg --ref logo.png            # ajoute SSIM / PSNR / ΔE OKLab
$ svgstat before.svg after.svg               # delta de toutes les métriques de coût
$ svgstat logo.svg --json                    # pour la CI
$ svgstat logo.svg -format row -category logo -name foo   # une ligne de tableau
```

Il rapporte le **coût** (octets, octets gzip, éléments, nœuds, commandes de path,
buckets de précision décimale, points approximatifs après aplanissement) et, si
on lui donne un raster de référence, la **fidélité** (SSIM, PSNR, distance
moyenne en OKLab, RMSE pondéré par le gradient). Le rendu de référence est produit
par un oracle externe (`inkscape` ou `resvg`) : le cœur de la lib reste sans
dépendance.

**Ce qui compte, dans cet ordre :**

- **la fidélité** — le RMSE OKLab pondéré par le gradient est la métrique de
  classement. Le gradient concentre l'erreur sur les bords, qui est ce que l'œil
  remarque ; sur une géométrie identique, le ΔE plat vaut 0.0102 alors que le
  score pondéré vaut 0.205, parce que toute l'erreur est sur les arêtes.
- **le nombre de nœuds et de points** — la seule contrainte dure. Ce n'est pas
  de la compression (voir `docs/reflexion.md` §10.1) : c'est ce que le moteur de
  rendu manipule à chaque frame, et c'est ce qui rend la partie vidéo possible ou
  non.
- **les octets** — rapportés parce que c'est gratuit, jamais optimisés. La
  compression est un problème d'empaquetage, résolu tard (D6).

## L'encodeur : `svgimg`

```console
$ svgimg -in logo.png -mode embed                            # 1 noeud, lossless
$ svgimg -in logo.png -mode grid -tile 8                    # 1 rect par tuile
$ svgimg -in logo.png -mode runlength -k 16 -max-nodes 2000 # 1 rect par suite
$ svgimg -in photo.png -mode runlength -stdout > out.svg
$ svgimg -in logo.png  -mode trace -k 8 -max-nodes 5000   # contours, budget de noeuds
```

Les trois modes sont les **points de comparaison honnêtes**, pas des
fonctionnalités : `embed` est l'hypothèse nulle (le raster dans un `data:` URI),
`grid` et `runlength` sont « la meilleure des options naïves ». Les mesurer est
ce qui donne au traceur de contours quelque chose à battre.

`-max-nodes` est le budget de D6. Sur `grid` il **grossit** les tuiles plutôt que
de tronquer, donc l'image reste entièrement couverte ; sur `runlength` il
**tronque** et le signale, avec la couverture obtenue.

## Makefile

`make help` liste tout. Rien que du toolchain Go n'est requis ; l'oracle de rendu
est optionnel et son absence dégrade la mesure au lieu de la faire planter.

```console
make              # fmt, vet, test, build
make ci           # ce que la CI doit exécuter (fmt-check, vet, tidy-check, test-race)
make help         # toutes les cibles, avec ce que font les outils détectés

make oracle-check # l'oracle de rendu est-il disponible ?
make demo         # construit un SVG + son raster de référence, et les mesure
make stats FILE=x.svg [REF=x.png]

make convert          # ré-encode assets/png dans assets/svg, tous modes
make bench-baselines  # mesure assets/svg dans docs/benchmarks.md

make corpus-render # re-rend chaque testdata/corpus/*/*.svg depuis le SVG
make corpus        # couverture du corpus par catégorie
make corpus-bench  # trace puis mesure le corpus dans docs/corpus-bench.md
make bench-table   # mesure les SVG du corpus dans docs/benchmarks.md
make fuzz          # fuzz du parseur SVG (FuzzAnalyze)
make cover lint bench test-race
make clean
```

`golangci-lint` et `staticcheck` sont détectés et **sautés** s'ils sont absents :
un linter manquant ne doit jamais pouvoir faire échouer un build. `gofmt -l` et
`go vet` sont le plancher garanti.

## Structure visée


Chaque étage du pipeline est un paquet, avec des **structs Go en entrée et en
sortie — jamais de chaînes SVG avant la toute dernière étape**. Une fonction
pure par étage, donc remplaçable et testable isolément.

```
internal/pixelbuf   décodage, buffer planaire, sRGB -> linéaire -> OKLab, alpha
internal/quant      median-cut en OKLab, sans tramage
internal/field      lissage anisotrope, gradient, magnitude
internal/contour    marching squares sous-pixel, topologie, imbrication
internal/simplify   Douglas-Peucker à tolérance pondérée par le gradient
internal/fitcurve   Selinger quadratique, fallback cubique
internal/pathdata   construction et minimisation des `d`
internal/dom        l'arbre SVG interne
internal/encode     sérialisation, groupement par couleur
internal/budget     budget de nœuds et échelle de dégradation

cmd/svgstat         l'instrument      cmd/svgimg   la lib   cmd/svgvid  la vidéo
```

Déjà présent :

| paquet | rôle |
|---|---|
| `internal/colorconv` | sRGB ↔ linéaire ↔ OKLab, validé sur les valeurs de référence |
| `internal/svgstat` | la mesure : coût + fidélité |
| `internal/imcompare` | PSNR, SSIM, ΔE OKLab, MSE pondérée par le gradient |
| `internal/pixelbuf` | buffer planaire, alpha straight, sRGB → OKLab |
| `internal/quant` | median-cut en OKLab, alpha-pondéré, sans tramage |
| `internal/dom` | l'arbre SVG interne : structs Go, attributs ordonnés |
| `internal/baseline` | les trois encodeurs de référence + le budget de nœuds |

## Commandes

```console
make            # ou, sans make :
go test ./...
go run ./cmd/svgstat <fichier.svg> [--ref <raster>]
go run ./cmd/svgimg -in <raster.png> -mode runlength -out <sortie.svg>
go run ./cmd/svgimg -in <raster.png> -mode trace -k 8 -max-nodes 5000 -out <sortie.svg>

# aplatir la source sur un fond connu avant de vectoriser, et mesurer des deux
# côtés sur ce fond. Sans cela l'alpha ne peut pas être restitué : le traceur ne
# porte l'opacité que par forme, et l'alpha de la source varie le long de chaque
# bord. Le défaut préserve l'alpha, parce qu'aplatir dessine le fond dans le SVG.
go run ./cmd/svgimg -in <raster.png> -mode trace -bg white -out <sortie.svg>
go run ./cmd/svgstat <sortie.svg> --ref <raster.png> -bg white
```

## Statut

| phase | contenu | état |
|---|---|---|
| 0 | instrument de mesure | **fait** — corpus de 7 cas dans `testdata/corpus/<cat>/<nom>.{svg,png}`, mesuré dans [docs/corpus-bench.md](docs/corpus-bench.md) |
| 1 | baselines (encapsulation, grille, run-length) | **fait** — mesuré sur `assets/png`, voir [docs/phase1.md](docs/phase1.md) |
| 2 | traceur de contours — le cœur | **fait** — 4 à 17 éléments DOM au lieu de 68 à 2 247 sur le corpus, voir [docs/phase2.md](docs/phase2.md) |
| 3 | modes (pixel art, screenshot+texte, trait, photo) | à faire |
| 4 | sémantique, accessibilité, IDs stables | à faire |
| 5 | vidéo (tracking, delta, animation par transformation) | à faire |
| 6 | outillage (CLIs, WASM, CI, docs) | à faire |

## Licence

À définir.

