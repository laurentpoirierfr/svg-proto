# Phase 2 — traceur de contours

## Ce que la phase apporte

Un pipeline complet, déterministe et sans dépendance externe :

```
pixelbuf → quant → field → contour → simplify → encode → dom
```

Quatre paquets nouveaux, tous couverts par des tests :

| paquet | rôle | couverture |
|---|---|--:|
| `internal/field` | champ scalaire, gradients, diffusion anisotrope Perona–Malik | 93,7 % |
| `internal/contour` | marching squares, décision asymptotique, chaînage | 97,7 % |
| `internal/simplify` | suppression des colinéaires + Douglas–Peucker pondéré | 89,6 % |
| `internal/encode` | bandes empilées, alpha, budget de nœuds, path data | 95,7 % |

CLI : `svgimg -mode trace -k 16 -max-nodes 20000`.

## Résultats mesurés

Table complète dans [benchmarks.md](benchmarks.md). Sur les deux images du dépôt,
toutes deux photographiques :

| image | encoder | éléments DOM | points | ssim | ΔE OKLab (score) |
|---|---|--:|--:|--:|--:|
| des | embed | 2 | 0 | 1,0000 | 0,0000 |
| des | grid | 8 997 | 0 | 0,8387 | 0,0891 |
| des | runlength | 35 693 | 0 | 0,8820 | 0,0658 |
| des | **trace** | **17** | 49 469 | 0,8570 | **0,1014** |
| tux | embed | 2 | 0 | 1,0000 | 0,0000 |
| tux | grid | 3 054 | 0 | 0,6982 | 0,1099 |
| tux | runlength | 13 193 | 0 | 0,8276 | 0,0611 |
| tux | **trace** | **17** | 22 996 | 0,7180 | **0,1051** |

Sur les photos, le traceur est désormais plus proche de `runlength` en ΔE
qu'avant la correction de palette, tout en restant 500 à 2 000 fois moins
gênant en structure DOM.

### Lecture honnête

Le traceur **gagne enormously sur la structure et perd sur la fidélité** :

- 17 éléments DOM contre 8 997 à 35 693, soit 500 à 2 000 fois moins. C'est
  l'éditabilité qui est la raison d'être du projet (D2), et elle est atteinte.
- Il est **moins fidèle** que `runlength` sur les deux images, et à peine plus
  fidèle que `grid` sur `des`. Sur une photo, les iso-contours à 16 niveaux sont
  intrinsèquement fractals : aucun budget de nœuds ne les rend à la fois compacts
  et fidèles. C'est la conclusion de la Phase 1, pas une surprise.
- `score` est une **erreur** (RMSE OKLab pondéré par gradient, plus bas est mieux),
  pas un score composite coût/fidélité. Le traceur y reste moins bon que les deux
  baselines sur ces deux images.

Ces images étant photographiques, elles ne valident pas la cible v1 (D2 :
flat / UI / logos). Le corpus ci-dessous est la donnée qui compte pour la v1.

### Sur le corpus, la structure est gagnée, les octets sont partagés

Table complète dans [corpus-bench.md](corpus-bench.md), sept cas, trois encoders.

| cas | points | éléments | gzip trace / runlength | score trace / runlength |
|---|--:|--:|--:|--:|
| logo/mark | 17 827 | 17 | 55 605 / 8 974 | 0,1466 / **0,0258** |
| logo/mark-scaled | 2 897 | 17 | **11 947** / 39 114 | 0,0812 / **0,0140** |
| icon/gear | 6 426 | 9 | 20 519 / 4 193 | 0,1036 / **0,0199** |
| ui/panel | 1 365 | 13 | **4 100** / 3 146 | 0,1490 / **0,0133** |
| lineart/glyph | 1 089 | 5 | **3 861** / 3 959 | 0,1390 / **0,1126** |
| pixelart/blob | 58 | 4 | **400** / 700 | 0,2492 / **—** |
| alpha/badge | 714 | 7 | 2 314 / **1 761** | 0,5847 / **0,0285** |

Trois choses à y lire honnêtement :

- **La structure est gagnée partout**, et c'est très large : 4 à 17 éléments DOM
  contre 68 à 2 247, soit 20 à 150 fois moins. C'est la raison d'être du projet.
- **La fidélité est perdue partout**, de 0,013 à 0,21 de score. `runlength` encode
  la grille 2D, donc il a toute l'information de l'image ; le comparer à un
  traceur n'est pas un concurrent, c'est une borne supérieure. Sur `ui/panel`,
  la cible v1 réelle, l'écart est 0,136 : c'est là que se trouve le travail.
- **Les octets sont partagés, pas gagnés.** `logo/mark` sort à 6,2× le gzip de
  `runlength` : 17 827 points à une décimale coûtent plus cher que des segments
  courts, et un tracé de contour est une description de frontière là où
  `runlength` reste une description de grille. Le gain de compacité n'apparaît que
  quand l'image est grande par rapport à son contour — `mark-scaled`, `lineart`,
  `ui/panel`, `pixelart` sont tous gagnants. D6 arbitre les points avant les
  octets, et les points sont gagnés partout ; l'octet, lui, dépend de l'échelle
  de l'image et devra être traité par le budget de points.

## Trois bugs que seule la mesure a révélés

### Ordre de peinture des bandes

Les régions sont imbriquées : la bande `i` couvre tout le niveau `i+1` et
au-dessus, donc la bande `i+1` est strictement à l'intérieur. Il faut peindre de
la plus grande à la plus petite, c'est-à-dire **du plus sombre au plus clair**.

Peindre dans l'autre sens enterre chaque bande claire sous une bande plus grande
et plus sombre. Le document reste valide, le compte de nœuds est inchangé, et
rien ne plante : seule la comparaison pixel à pixel le montre. Le ΔE est passé de
**0,3194 à 0,1273** sur `des` et de **0,3556 à 0,1119** sur `tux` en corrigeant
l'ordre. `TestBandOrderPaintsBrightOnTopOfDark` verrouille l'invariant.

### Le sentinel de transparence cassait la pondération

Les pixels sous le seuil d'alpha reçoivent la valeur `-1e9` pour être exclus de
toute région tracée. C'est un tours de force, pas une couleur. Utiliser ce même
champ pour dériver les poids de simplification donne un gradient de 1e9 sur le
bord de chaque région transparente, donc un budget d'erreur nul, donc **rien ne
se simplifie** — et le budget de nœuds passe pour ignoré quoi qu'on lui fasse.

Le symptôme (un document qui ignorerait son budget quel que soit le budget)
pointait vers la boucle de budget ; la cause était dans les poids. Le champ de
sélection et le champ de luminosité sont désormais deux champs distincts.

### Le composite de l'instrument était faux pour tout alpha partiel

`imcompare` compositait la référence et le rendu sur un fond avant de mesurer,
ce qui est la seule façon de comparer honnêtement une image transparente. Le
calcul, lui, était faux — et faux d'une manière qui ressemblait à une
dé-prémultiplication correcte.

La valeur premultipliée d'un pixel est **déjà** `couleur × couverture`. Le
correctif est donc `prémultiplié + fond × (1 − couverture)`, sans rien dé-prémultiplier.
Le code divisait par la couverture pour retrouver la couleur droite, puis
ajoutait la part du fond **sans pondérer le terme retrouvé** :

```
0.5 × 255 + 255 × 0.5 = 255        ce qu'il faut
255   + 255 × 0.5     = 382        ce que le code faisait
```

Les deux cas limites sont corrects — `couverture = 0` renvoie le fond,
`couverture = 1` renvoie la couleur — et les deux étaient testés. Seule
l'alpha partiel, c'est-à-dire tout le reste, était faux. **Toutes les mesures
d'alpha du projet étaient fausses**, y compris celles des deux photos, dont les
rendus ont de l'alpha partiel : `des` est mesuré à 0,2409 et non 0,2573, et son
ssim passe de 0,8570 à 0,8629. `alpha/badge` passe de 0,5847 à 0,3059, un
facteur deux.

Le test qui le révèle ne teste pas la fonction, il teste une **propriété** : la
composition est idempotente, donc mesurer un rendu contre une référence
transparente et contre la même référence pré-composée doit donner les mêmes
nombres. Ils ne donnaient pas les mêmes, ΔE 1,434 contre 0,831, et il n'y avait
rien à voir avec la qualité du traceur. `TestComparisonIsIndependentOfHowTheReferenceWasStored`
épingle la propriété, avec une tolérance calibrée entre l'arrondi 8 bits de
l'intermédiaire (≈1e-3) et la signature du bug (≈6e-1).

Le correctif a déplacé la règle dans `colorconv.CompositeOver`, partagée par
l'encodeur et le comparateur, parce que le vrai risque n'était pas l'erreur mais
le fait qu'elle existe en deux endroits.

## Le corpus, et ce qu'il a révélé immédiatement

`assets/png` ne contient que deux photos, et la Phase 1 l'avait déjà établi :
elles ne sont pas la cible. Elles ont d'abord été laissées hors du corpus, et ce
fut une **erreur de mesure** : sans contenu photographique, chaque ratio publié
décrivait de l'art plat, où le traceur est avantagé. Les deux photos sont
désormais les cas `photo/des` et `photo/tux`, non pas comme cible mais comme
borne : c'est le contenu le plus dur, et il faut savoir ce que le mode y vaut
plutôt que de l'extrapoler.

Le corpus est dans `testdata/corpus/<categorie>/<nom>.svg` + `<nom>.png`, avec
`make corpus-render` pour régénérer les rasters depuis les SVG. Les SVG d'origine
sont écrits à la main et font foi : le raster est **dérivé** du SVG, pas
collecté, donc un désaccord entre les deux est un bug du harness et non une
tolérance à documenter. Vérifié sur les cas écrits à la main : `embed` rejoue
chaque raster à l'identique (AE=0), sauf `alpha/badge` — attendu, `embed` ne porte qu'un seul rectangle, et
c'est précisément le cas que `trace` doit rendre via `fill-opacity`.

Les deux cas `photo/` sont une exception : ils n'ont pas de SVG source, le PNG
**est** la source, et `make corpus-render` les ignore puisqu'il ne rend que les
`.svg`. C'est la catégorie `photo` que `CORPUS_CATS` prévoyait déjà.

Une seconde exception est l'apport tiers : dix-huit cas viennent de Feather
Icons et de Bootstrap Icons, sous MIT, avec l'attribution complète dans
[`ATTRIBUTION.md`](../testdata/corpus/ATTRIBUTION.md). `width` et `height` ont
été portés à 256 pour rendre à une taille comparable, et `currentColor` remplacé
par `#000` pour ne pas dépendre du défaut du moteur ; les tracés sont intacts. Ils restent dérivés du SVG et rejouables par
`make corpus-render`, mais ils ne sont pas écrits à la main : la règle « le SVG
fait foi » vaut pour eux aussi, la provenance en plus.

| cas | ce qu'il teste |
|---|---|
| `logo/mark` | logo plat : anneau, rectangle, triangle acutangle, cercles |
| `logo/mark-scaled` | le même logo rendu à 1024 px, pour exposer le chanfrein à l'échelle |
| `icon/gear` | 8 dents à angles droits, un trou |
| `ui/panel` | la cible v1 : rectangles, filet 1 px, coins arrondis |
| `lineart/glyph` | traits minces à fort contraste |
| `pixelart/blob` | aligné sur la grille de pixels, aucun antialiasing |
| `alpha/badge` | semi-transparence : 53 % de l'image en alpha nul |
| `photo/des` | photographie 800×600, 89 060 couleurs : la borne du rendu fidèle |
| `photo/tux` | photographie 386×395, 12 832 couleurs : le cas où le plafond ne peut pas mordre |
| `lineart/{activity,anchor,compass,git-branch,scissors,umbrella,wind}` | Feather (MIT) : traits fins, courbes et diagonales |
| `icon/{bezier,bezier2,bounding-box-circles,diagram-3,hurricane,infinity,moisture,vector-pen}` | Bootstrap (MIT) : courbes, spirales, remplissages |
| `ui/{grid-3x3-gap,sliders,window-sidebar}` | Bootstrap (MIT) : glyphes d'interface |

Le premier résultat a été le plus mauvais score de tout le projet :
`alpha/badge` à 0,5485, **pire que `grid`**, et vingt fois pire que `runlength`.

### La palette se dépensait sur le bruit

Le même logo, quantifié en 16 couleurs, donnait 11 entrées : deux couleurs
réelles et **9 niveaux de gris pour 130 pixels** d'un bord antialiasé, pendant que
le bleu marine (952 px) et le rouge (6126 px) n'en obtenaient qu'un chacun.

La cause n'était pas la table de décision, ni le choix de l'axe : c'est que
`volume` classait les boîtes à splitter sur leur **étendue géométrique seule**. Un
logo plat donne quelques grandes surfaces, chacune dans une seule cellule
d'histogramme donc d'étendue nulle, et une frange antialiasée qui est une chaîne
de cellules quasi identiques étalées sur une large plage de clarté, donc
d'étendue énorme. Chaque split tombait sur la frange. Instrumenté :

```
split  1: share=1.0000   split  5: share=0.0216
split  2: share=0.4273   split  6: share=0.0107
split  3: share=0.1731   split  7: share=0.0053
split  4: share=0.0235   split  8: share=0.0053
```

Sept des dix splits partaient sur des boîtes de moins de 2,4 % de l'image.

Pondérer `volume` par la population ne corrige rien, et la raison mérite d'être
notée : les couleurs plates classent à étendue **nulle**, elles ne perdent pas la
comparaison, elles n'y sont pas. Aucune pondération ne rend une boîte d'une seule
cellule digne d'être coupée, puisqu'elle est *déjà* exactement une couleur.

Le correctif est un plancher de split, dans `medianCut` : une boîte de moins de
`min(MinSplitPixels, N/100)` pixels n'est pas coupée. Il ne s'agit que de
**refuser de créer** une boîte — rien n'est fusionné, aucun pixel n'est
réaffecté — donc les petits traits gardent leur couleur et le `MinArea` de
l'encodeur reste maître de savoir s'ils méritent d'être dessinés. La borne
supérieure compte : un plancher purement relatif à 1/(2K) se comporte correctement sur
un badge de 128×128 puis casse sur le même artwork rendu en 1024×1024, où « 1,5 %
de l'image » fait 31 000 pixels ; refuser de couper une boîte de cette taille
grossit toutes les bandes jusqu'à transformer les contours en escaliers, et
`logo/mark-scaled` est passé de 2 896 à 16 680 points, une régression de 5,8× sur
le meilleur résultat du corpus. Un plancher purement absolu casse à l'autre
extrémité : sur une image de 27 pixels dont les trois couleurs en font 9 chacune il
bloque les splits et réduit trois couleurs à une.

`K` devient donc un majorant et non une cible : une image à trois couleurs
demandée à seize en rend trois, et `Palette.Len()` le dit sans qu'on ait à le
deviner.

### `prune` a été supprimé après mesure

Un mécanisme de fusion par distance de couleur avait été ajouté pour absorber
les entrées de palette indiscernables plutôt que redondantes. Il n'a **jamais
fusionné** : `Merged = 0` sur les sept cas d'origine, puis sur les trente-trois
cas du corpus étendu, sur les deux photographies et sur un dégradé synthétique,
à 0,02 comme à 0.

La mesure a aussi corrigé sa justification. Le problème qui l'avait motivé — la
frange antialiasée de `alpha/badge` accaparant neuf entrées sur onze — n'était
pas un problème de doublons : `Merged` y valait déjà 0, et c'est `MinSplitPixels`
qui l'avait réglé. Et `Palette.Merged` ne mesurait pas les « slots inutilisés »
(ceux-là valent `K - Len()`), il mesurait les fusions.

Il était donc le seul mécanisme du projet conservé sur une probabilité et non sur
une mesure. Le corpus étant passé de 7 à 33 cas, la condition posée pour le
rejuger était remplie : il a été **supprimé**, avec son champ `Merged` et ses
tests. Ce qui reste de son passage, ce sont deux leçons d'implémentation :

- `append` en place écrasait la boîte voisine. `bisect` renvoie `b[:h]` et
  `b[h:]`, deux vues du même tableau ; absorber une boîte dans une autre
  détruisait la troisième, et le carré rouge du badge virait au bleu marine.
- Un seuil fondé sur l'aire volait le travail de `MinArea` : un point blanc d'un
  pixel sur 40×20 pèse 0,125 % de l'image et était absorbé dans le noir au niveau
  de la palette, avant que `MinArea` ne voie la région. C'est pourquoi
  `TestThinDistinctColourKeepsItsEntry` garde la ligne d'un pixel dans la palette
  et laisse l'encodeur décider.

### Ce que ça a changé

| cas | points avant / après | ΔdE |
|---|---|--:|
| lineart/glyph | 2508 → 1089 (−57 %) | = |
| ui/panel | 1973 → 1365 (−31 %) | −0,0005 |
| icon/gear | 7346 → 6426 (−13 %) | = |
| logo/mark | 18641 → 17827 (−4 %) | −0,0006 |
| des (photo) | 35167 → 49469 | **−0,0259** |
| tux (photo) | 26191 → 22996 (−12 %) | −0,0068 |

Nœuds en baisse partout, fidélité inchangée ou meilleure : c'est exactement ce
que D6 demande. `alpha/badge` est le seul cas qui se dégrade, 0,5485 → 0,6143, et
c'est le sujet de la limite suivante.

## Limites connues

### Coins vifs : corrects, et pas rentables

Le traceur-emprise coupait chaque angle en biseau. Dans la cellule d'angle, un
seul coin est à l'intérieur, et le contour reliait les deux milieux d'arête par
une diagonale. C'était mathématiquement correct pour l'interpolateur utilisé et
c'était faux pour la région : sous la convention d'échantillon centré, un
échantillon isolé couvre exactement le quart de sa cellule délimité par les deux
points de croisement et le **centre** de la cellule. Son contour fait donc deux
demi-arêtes qui se rencontrent à angle droit au centre, et la diagonale coupe
l'angle.

La mesure était sans appel : un carré de 8 px sortait à 63,5 px² de surface et à
30,83 de périmètre, ce qui n'est pas un carré. Après correction : 64 px², périmètre
exactement 32, quatre angles droits aux points `(3.5,3.5)`, `(11.5,3.5)`,
`(11.5,11.5)`, `(3.5,11.5)`, et aucune arête diagonale. `TestCornersAreSharp`
épingle l'aire, le périmètre, la présence des quatre coins *et* l'alignement
des arêtes, parce que l'aire seule passerait aussi pour un contour qui remplace
chaque angle par une détour plus court ailleurs.

Le A/B sur le corpus dit however que le correctif ne paie pas :

| cas | points vifs / chanfrein | ΔdE |
|---|---|--:|
| icon/gear | 7346 / 5064 (+45 %) | −0,0006 |
| lineart/glyph | 2508 / 1753 (+43 %) | −0,0005 |
| logo/mark | 18641 / 13095 (+42 %) | −0,0009 |
| pixelart/blob | 58 / 48 (+21 %) | −0,0006 |
| logo/mark-scaled | 2896 / 6346 (−54 %) | −0,0005 |

Le ΔE s'améliore partout sauf `alpha` et `ui/panel`, mais de **0,0005**, contre
**+43 % de points**. Le gain est dérisoire et le coût ne l'est pas : à l'échelle
native un chanfrein d'un demi-pixel est invisible.

La correction est **gardée** quand même, pour trois raisons. Elle est
géométriquement juste, et un carré doit être un carré. Le test
`TestCornersAreSharp` vaut mieux que l'affirmation qu'il remplace. Et sur
`logo/mark-scaled`, où les coins sont grands et réels, elle *réduit* les points
de 54 % : les coins vifs remplacent des escaliers en diagonale qu'il fallait
payer point par point. Ce qui ne paie pas, ce sont des points payés pour du bruit
de quantification, pas pour de la géométrie.

### Semi-transparence : deux sorties, une par défaut

`alpha/badge` plafonne à **0,3059**, et ce n'est pas un bug mais la conséquence
de l'architecture. L'alpha n'est pas une dimension de la palette : il est porté
par `fill-opacity` par forme, et une forme n'a qu'une opacité. Or l'alpha de la
source **varie le long de chaque bord** — c'est exactement ce que
l'antialiasing est. La bande qui recouvre une frange mélange des pixels dont
l'alpha réel va de 0,35 à 1, et sa `fill-opacity` est une moyenne : empiler ces
bandes étale la transparence au lieu de la restituer.

Le diagnostic est propre. Le même badge forcé opaque score 0,9363 de ssim et
0,0296 de ΔE ; avec son alpha il tombe à 0,7320 et 0,1878. Le contenu n'est
donc pas en cause, le modèle de l'alpha l'est.

**Voie 1, implémentée : composer sur un fond connu.** Une image transparente
n'a pas d'apparence tant qu'on n'a pas dit sur quoi elle est posée. `-bg` aplatit
la source avant vectorisation, et la mesure compose les deux côtés sur le même
fond. Le résultat :

| variante | score | ΔE | points | éléments | octets |
|---|--:|--:|--:|--:|--:|
| alpha préservé (défaut) | 0,3059 | 0,1878 | 714 | 7 | 8 935 |
| `-bg white` | 0,1112 | 0,0186 | 565 | 6 | 7 072 |
| `-bg black` | 0,0834 | 0,0105 | 529 | 8 | 6 759 |

Tout s'améliore à la fois : fidélité, points, octets. Le ΔE est divisé par dix.

Le fond domine tout le reste, ce qui rend la cohérence entre les deux
indispensable : `-bg black` mesuré sur fond blanc vaut 0,7737, non parce que
l'encodage est mauvais mais parce qu'on compare un rendu posé sur du noir à une
référence posée sur du blanc. `svgstat` a donc le même `-bg`, résolu par le
**même** parseur que `svgimg` — deux chaînes qui divergent produisent une mesure
fausse sans la moindre erreur. `make corpus-bench` émet les deux lignes, `trace`
et `trace-bg`, et sur les six cas opaques elles sont identiques au bit : le
contrôle est gratuit et permanent.

**Le défaut préserve l'alpha.** Aplatir n'est pas une approximation, c'est un
changement de contenu : `-bg white` dessine un fond blanc opaque dans le SVG, et
pour un logo dont la transparence *est* le contenu, c'est une perte
irréversible faite sans qu'on l'ait demandée. L'aplatissement reste donc
explicite, et la note honnête est que la sortie par défaut est la moins bonne des
deux — elle est la seule qui ne détruit pas l'entrée.

**Voie 2, non retenue : mettre le niveau d'alpha dans la clé de bande**, pour
qu'aucune bande ne mélange deux opacités. Plus fidèle, et cela multiplie les
entrées de palette par le nombre de buckets d'alpha — un non-chiffrement que
`quant` documente déjà. La mesure dit que cette décision était fausse pour les
images transparentes : c'est le pire score du projet. À garder en réserve si la
voie 1 ne suffit pas.

### Jonction à quatre voies quand un nœud est exactement au seuil

Un nœud dont la valeur est *exactement* le seuil compte comme intérieur, et les
quatre cellules qui l'entourent émettent chacune un arc à travers ce nœud. Le
niveau se recoupe et `chain` ne peut pas choisir de successeur. Les boucles
ouvertes sont **abandonnées** plutôt qu'émises, parce qu'un sous-chemin non
fermé se rend avec un gap visible.

Injoignable en production : un seuil est le milieu de deux valeurs de palette
distinctes, donc aucun échantillon ne peut l'atteindre.
`TestNodeExactlyOnLevelFormsAFourWayJunction` documente le cas plutôt que de le
corriger.

### Béziers : le fitter marche, l'intégration a perdu puis a été réparée

`internal/curvefit` est écrit, testé et correct sur sa propre entrée. Un cercle de
724 points y devient **8 cubiques, 24 points, erreur 0,26 px** pour une tolérance de
0,4. Les coins sont préservés, un côté droit reste un `L`, les segments s'enchaînent
sans trou et le résultat ne dépend pas du vertex de départ.

Branché sur `trace` derrière `-curves`, il perdait d'abord sur les sept cas du
corpus, et pire sur un cercle parfait. Deux causes, toutes deux en amont du fitter.

**Le fitter recevait une marche d'escalier.** Un cercle rasterisé, tracé par
marching squares, est une suite de marches d'1 px. Chaque marche est un angle
droit, donc la détection de coins la classait en coin à juste titre — arrondir un
angle droit, c'est transformer un disque en pastille. Les cubiques qui en sortaient
le montraient sans interprétation : `C26.50 31.62 26.50 29.88 26.50 31.00` a son
point de contrôle **derrière** ses extrémités et fait demi-tour.

**Et le poids de gradient rendait `-tol` inerte.** Dans `simplify` :

```
budget = Tolerance * Reference / (Reference + gradient)
```

Sur un bord dur le gradient vaut environ 1,4 pour `Reference = 0,05`, donc la
tolérance effective est divisée par un facteur ~30. Mesuré sur le disque, le chemin
pondéré rend **288 points à toute tolérance de 0,25 à 4** : le curseur ne commande
rien du tout. C'est voulu pour protéger les arêtes dures, mais sur une frontière
qui est dure *partout*, le mécanisme protège la marche d'escalier au lieu de
l'absorber.

La correction est de ne pas peser sur le chemin courbé, parce qu'un chemin qui
remporte les courbes a d'abord besoin que la marche disparaisse :

| disque 256×256, r=100 | points `trace` | points `-curves` |
|---|--:|--:|
| chemin pondéré, `-tol 0,25` à `4` | 288 (constant) | — |
| non pondéré, tolérance 1 | 288 → **33** | 41 nœuds en 6 cubiques |

Le poids reste sur le chemin lignes, où il fait ce qu'il a été construit pour. La
simplification du chemin courbé a sa propre tolérance, `-curve-simplify`, valant 1
px par défaut : ce n'est pas la même grandeur que la tolérance d'ajustement, l'une
efface l'escalier de tracé et l'autre remet de la courbure.

**Un bug de plus est tombé en route.** `Options.withDefaults` ne gave aucune valeur
par défaut aux options de courbe, donc une `Options{}` de bibliothèque laissait
`CurveSimplifyTolerance` à **zéro**. Zéro n'est pas une tolérance serrée mais
l'absence de tolérance : Douglas-Peucker renvoyait alors tous les points tracés, et
le fitter recevait l'escalier complet. Rien n'échouait bruyamment, la sortie
ressemblait à des courbes. C'est maintenant couvert par un test de régression.

Un point est resté hors de portée pendant plusieurs mesures, et il a fini par se
révéler être la faute de la mesure elle-même. Là où une marche rejoint un côté
droit, le coin n'était pas trouvé : la marche voisine tourne de 45° sur un pixel
et se trouve donc à moins de 2 px, ce qui suffisait à écarter le coin. Rendre
`QuietAngle` plus permissif réparait ce cas et en cassait d'autres, et c'est ce
qui a fait longtemps prendre le problème pour un choix de seuil.

La cause était que `turnAngles` renvoyait l'angle **non signé**. Une marche est un
zigzag : ses pas tournent −90, +90, −90, +90, chacun annulé par le suivant, pendant
que l'angle absolu à chaque sommet affiche 90 — exactement ce qu'affiche un vrai
coin. En magnitude seule, une marche est indiscernable d'une rangée d'angles
droits. Lire le signe sépare les trois cas :

- le coin en bout de marche a un **côté droit** d'un côté et un run alterné de
  l'autre ;
- un pas au milieu de la marche a des runs alternés des deux côtés et aucun côté
  droit, donc il n'a rien à quoi changer ;
- un sommet du disque n'a de côté droit d'aucun côté.

La règle est donc qu'un coin est un changement d'état : il lui faut un run
rectiligne d'au moins un côté, et de l'autre côté du droit ou un zigzag, mais
jamais une courbure soutenue. C'est ce dernier cas qui ressemblait à un coin sans
en être un.

Coût mesuré : la règle est plus stricte, donc elle trouve moins de coins, et le
corpus se dégrade très légèrement (`icon/gear` 0,1197 → 0,1212, `logo/mark` 0,1672
→ 0,1746, `pixelart/blob` et `alpha/badge` inchangés ou améliorés). La géométrie
est juste ; le corpus, qui exerce peu de diagonales, n'en profite pas. Les nœuds
sont neutres. C'est le compromis attendu d'une correction de justesse.

#### Ce que ça donne, mesuré

Corpus, `-max-nodes 4000`, `-curve-tol 0.4`, `-curve-simplify 1` :

Les colonnes ci-dessous sont générées depuis `docs/corpus-bench.md` plutôt que
saisies, la main s'étant déjà trompée une fois entre octets bruts et octets gzip.

| cas | octets `trace` | octets `-curves` | ratio | nœuds `trace` | nœuds `-curves` | score `trace` | score `-curves` |
|---|--:|--:|--:|--:|--:|--:|--:|
| `logo/mark` | 234 590 | 19 089 | 12,3× | 17 827 | 2 299 | 0,1466 | 0,1746 |
| `icon/gear` | 79 411 | 7 663 | 10,4× | 6 426 | 954 | 0,1036 | 0,1212 |
| `lineart/glyph` | 14 658 | 2 040 | 7,2× | 1 089 | 174 | 0,1390 | **0,1389** |
| `alpha/badge` | 8 935 | 1 827 | 4,9× | 714 | 222 | 0,3059 | **0,3051** |
| `ui/panel` | 17 967 | 6 999 | 2,6× | 1 365 | 655 | 0,1490 | 0,1697 |
| `pixelart/blob` | 949 | 606 | 1,6× | 58 | 27 | 0,2492 | 0,2560 |
| `logo/mark-scaled` | 40 098 | 42 961 | 0,9× | 2 897 | 4 775 | 0,0812 | 0,0905 |

Les nœuds sont la contrainte dure de D6, pas les octets : ils ne se compriment pas.
Sur ce critère, `-curves` gagne sur six cas sur sept, jusqu'à 12×, et
`logo/mark-scaled` est le seul où il en perd.

#### Six cas diagonaux, et ce qu'ils disent du correctif de signe

Le corpus n'a presque pas de diagonales, et c'est exactement la géométrie que le
correctif de signe visait. Six cas écrits pour ça sont donc ajoutés dans
`testdata/corpus/synthetic/`, catégorie nommée pour ce qu'elle est : du contenu
écrit pour le test, pas du contenu tiers. Ils sont rendus par l'oracle comme les
autres.

| cas | octets `trace` | octets `-curves` | nœuds `trace` | nœuds `-curves` | score `+` |
|---|--:|--:|--:|--:|--:|
| `diagonal-stroke` | 132 090 | 2 891 (46×) | 9 916 | 209 (47×) | +0,0195 |
| `diagonal-square` | 123 666 | 2 915 (42×) | 9 278 | 198 (47×) | **−0,0002** |
| `chevron` | 37 457 | 929 (40×) | 2 809 | 54 (52×) | +0,0154 |
| `tri-blade` | 130 341 | 3 918 (33×) | 9 664 | 276 (35×) | +0,0066 |
| `arrow` | 20 192 | 759 (27×) | 1 480 | 38 (39×) | **−0,0001** |
| `diag-stripes` | 29 430 | 1 226 (24×) | 2 226 | 108 (21×) | +0,0093 |

Là, l'écart est d'un tout autre ordre que sur le reste du corpus : 24× à 46×,
et le score ne bouge pas sur deux cas. Les formes à coins francs et bords
diagonaux sont précisément celles que la ligne gaspille le plus.

**Et le correctif de signe n'y change rien.** A/B sur ces six cas, nœuds et score
identiques au chiffre près avec la règle non signée. C'est un résultat qu'il
fallait mesurer plutôt que supposer, et il infirme ce que j'écrivais au commit
précédent.

La raison est simple une fois vue : la classification de coins ne départage la
marche d'escalier que si la marche est encore là. Or la simplification non pondérée
la supprime en amont, et ce que le fitter reçoit est déjà un polygone grossier —
198 points pour un carré de 256 px. Il n'y a plus de marches à confondre avec des
coins. Le correctif est juste, son test le prouve sur une boucle construite pour
ça, mais le pipeline ne lui présente jamais les conditions qui le rendaient
nécessaire. Sa légère dégradation sur l'ancien corpus est donc une perte sèche.

#### La frontière, et ce que `-curves` coûte vraiment

Une seule mesure ne permet pas de trancher : `-curve-simplify` et `-curve-tol` sont
deux réglages et le corpus n'en montrait qu'un point. Balayé sur les treize cas au
point de réglage du corpus (`k=16`, `-tol 0.25`, `-max-nodes 20000`), `curve-tol`
fixé à 0,4, nœuds **émis** :

| `-curve-simplify` | nœuds (13 cas) | ratio | Δscore moyen | Δssim moyen | pire Δssim |
|---|--:|--:|--:|--:|--:|
| 0,75 | 9 862 | 6,6× | +0,0121 | −0,0092 | −0,0319 |
| 1 | 5 798 | 11,3× | +0,0102 | −0,0074 | −0,0274 |
| **1,5** | **4 199** | **15,6×** | **+0,0103** | **−0,0083** | **−0,0263** |
| 2 | 3 905 | 16,8× | +0,0162 | −0,0127 | −0,0556 |
| 3 | 3 574 | 18,3× | +0,0196 | −0,0146 | −0,0569 |

**1 est dominé par 1,5** : même coût de fidélité, 15,6× au lieu de 11,3×. Le
défaut passe donc de 1 à 1,5. En dessous, le score monte plus vite que les nœuds
ne baissent ; au-dessus, plus vite encore. Ces ratios sont sur les nœuds émis ;
une version antérieure de ce tableau donnait 10,2× parce qu'elle comptait les
*points aplatis*, et l'erreur est la même que celle corrigée plus haut.

Il n'existe **presque aucun réglage gratuit**. À 1,5, sans plafond, en nœuds
émis, sur ces treize cas :

| cas | `trace` | `-curves` | ratio | Δscore | Δssim |
|---|--:|--:|--:|--:|--:|
| `synthetic/diagonal-stroke` | 9 896 | 95 | **104,2×** | +0,0177 | −0,0092 |
| `synthetic/diagonal-square` | 9 256 | 111 | **83,4×** | +0,0041 | −0,0014 |
| `synthetic/chevron` | 2 803 | 37 | **75,8×** | +0,0268 | −0,0174 |
| `synthetic/tri-blade` | 9 640 | 139 | **69,4×** | +0,0076 | −0,0025 |
| `synthetic/arrow` | 1 475 | 28 | 52,7× | **−0,0001** | +0,0010 |
| `logo/mark-scaled` | 87 260 | 1 802 | 48,4× | +0,0088 | −0,0029 |
| `synthetic/diag-stripes` | 2 214 | 54 | 41,0× | +0,0093 | −0,0043 |
| `logo/mark` | 21 833 | 966 | 22,6× | +0,0274 | −0,0216 |
| `icon/gear` | 6 405 | 409 | 15,7× | +0,0174 | −0,0263 |
| `lineart/glyph` | 1 077 | 104 | 10,4× | +0,0009 | −0,0017 |
| `alpha/badge` | 706 | 70 | 10,1× | +0,0019 | −0,0098 |
| `ui/panel` | 1 304 | 361 | 3,6× | +0,0058 | −0,0021 |
| `pixelart/blob` | 56 | 23 | 2,4× | +0,0068 | −0,0113 |

Un seul cas ne perd pas de score, `synthetic/arrow`, de 0,0001, ce qui est du
bruit ; les douze autres paient. La plage va de 2,4× à 104,2×, et le prix ne suit
pas le ratio : `pixelart/blob` paie +0,0068 pour 2,4× quand `synthetic/chevron`
paie +0,0268 pour 75,8×.

Les dix-huit icônes ajoutées depuis portent le corpus à trente et un cas hors
photographie, et le même calcul y donne **29,3×** (169 538 → 5 781), avec une
plage de 2,4× à 104,2× et trois cas qui ne paient pas. Elles ne changent donc
pas la loi, seulement la moyenne, qu'elles tirent vers le bas.

Ce qu'il faut comparer, c'est le même logo à deux résolutions, **sans plafond** :

| | lignes | courbes | ratio |
|---|--:|--:|--:|
| `logo/mark` 256×256 | 21 833 | 966 | **22,6×** |
| `logo/mark-scaled` 1024×1024 | 87 260 | 1 802 | **48,4×** |

Là, deux faits. Le chemin lignes croît **linéairement** : 4,0× de nœuds pour 4×
de résolution, exactement le rapport de périmètre, ce qu'attendrait n'importe
quel simplificateur qui travaille en pixels. Le chemin courbé croît
**sous-linéairement** : 1,87× pour 4×. Donc l'avantage de `-curves` **augmente**
avec la résolution, de 22,6× à 48,4×, au lieu de s'effondrer.

**J'ai donc publié deux fois la conclusion inverse de la bonne.** D'abord en
concluant qu'il fallait faire suivre la résolution par `-curve-simplify`, ce qui
n'était pas faux mais était la moitié de la histoire ; puis en concluant
l'inverse, que `-curves` était un outil de basse résolution. Les deux fois la
mesure portait sur la boucle de budget et pas sur le mode. Le mécanisme n'est
toujours pas établi — mais il n'est plus nécessaire pour décider, et la
propriété utile est simple et solide : **le chemin lignes paie la résolution au
proportionnel, le chemin courbé non.**

La mesure équitable est celle qui bride les deux modes au même plafond, ce que
fait `make corpus-frontier`. À 4 000 nœuds, `k=16`, `-tol 0,25`,
`-curve-tol 0,4`, `-curve-simplify 1,5` :

| cas | lignes | tol eff. | courbes | Δscore |
|---|--:|--:|--:|--:|
| `synthetic/arrow` | 1 475 | 0,25 | 28 | **−0,0001** |
| `synthetic/tri-blade` | 3 109 | **4,0** | 139 | +0,0034 |
| `synthetic/diagonal-square` | 3 263 | **4,0** | 111 | +0,0035 |
| `synthetic/diag-stripes` | 2 214 | 0,25 | 54 | +0,0093 |
| `synthetic/diagonal-stroke` | 3 203 | **4,0** | 95 | +0,0152 |
| `synthetic/chevron` | 2 803 | 0,25 | 37 | +0,0268 |
| `logo/mark-scaled` | 2 325 | **12,13** | 1 802 | +0,0035 |
| `lineart/glyph` | 1 077 | 0,25 | 104 | +0,0009 |
| `alpha/badge` | 706 | 0,25 | 70 | +0,0019 |
| `pixelart/blob` | 56 | 0,25 | 23 | +0,0068 |
| `ui/panel` | 1 304 | 0,25 | 361 | +0,0058 |
| `icon/gear` | 1 966 | **4,0** | 409 | +0,0152 |
| `logo/mark` | 1 155 | **10,61** | 966 | +0,0161 |

24 656 → 4 199 nœuds, soit **5,9×**, et le plafond passe de 47 % d'usage à 8 %.
La moyenne du Δscore est de **+0,0079**, et un seul cas s'améliore, `arrow`, de
0,0001, ce qui est du bruit.

Ce qui rend ce tableau lisible, c'est la colonne « tol eff. » : sur **six** cas
sur treize, la boucle de budget a escaladé la tolérance du mode lignes jusqu'à
4, 10,6 ou 12,1. Autrement dit, sur la moitié du corpus le mode lignes a été
**poussé vers la dégradation** pour tenir le plafond, pendant que le mode courbé
tourne à 0,25 sans jamais l'approcher. Et sur cinq de ces six cas, le mode
courbé coûte quand même de la fidélité, de +0,0034 à +0,0161. C'est le coût
honnête du mode : il n'est pas gratuit, même face à un adversaire dégradé.

Compte tenu de D6, la lecture d'ensemble est favorable : la contrainte dure est le
nombre de nœuds parce qu'elle ne se comprime pas, et sur cette contrainte le gain
va de 2,4× à 104,2× hors photographie, pour +0,0193 de RMSE OKLab en moyenne
(pire +0,0596, meilleur −0,0147) ; sur les deux photographies il tombe à 4,3× et
5,4×. La décision est prise : `-curves` reste **désactivé par défaut**, et se
documente par cette courbe, pas par un seuil (voir plus bas).

Les deux photographies, désormais cas du corpus, sont traitées en détail plus
bas. En nœuds émis à `k=16`, elles échangent 157 014 → 36 829 (4,3×) et
99 206 → 18 363 (5,4×) contre +0,006 et +0,017 de score, et surtout leur plafond
de nœuds ne peut pas être tenu.

La raison du défaut est mesurée, pas prudente. `-max-nodes` vaut 0 par défaut,
donc aucun budget ne presse ; dans ce régime le mode coûte de la fidélité sur 28
des 31 cas hors photographie, et un mode qui dégrade la fidélité en silence n'a
pas sa place dans un défaut. Il s'active explicitement, et l'amplitude du gain
dépend de la forme : c'est le premier mode où le contenu décide vraiment du
résultat.

Ce que cela dit du contenu tient toujours : `pixelart/blob` reste l'argument le
plus fort en faveur des Béziers, `grid` le restituant exactement (ssim 1,0000)
pendant que `trace` plafonne à 0,8392.

### `-tol` est lisse, et le tableau qui disait le contraire mesurait autre chose

Le tableau publie dans le commit precedent, avec son plateau a 17 783 noeuds et
ses inversions, **etait faux**. Il mesurait la boucle de budget, pas le
simplificateur. Avec `-max-nodes 20000`, la boucle escalade la tolerance jusqu'a
passer sous le plafond, donc la sortie est plaquée contre le plafond et sa
forme reflete les iterations de la boucle. Il faut mesurer **sans** plafond. Le
veritable, `k=16`, chemin lignes :

| `-tol` | mark 256 | panel | mark 1024 |
|--:|--:|--:|--:|
| 0,001 | 23 941 | 1 548 | 95 000 |
| 0,0625 | 23 481 | 1 481 | 92 536 |
| 0,25 | 21 833 | 1 304 | 87 260 |
| 1 | 17 783 | 995 | 71 975 |
| 4 | 8 174 | 539 | 35 396 |
| 5 | 5 035 | **464** | 23 243 |
| 6 | 2 815 | 505 | 11 368 |
| 8 | 1 419 | 465 | 3 033 |
| 10 | 1 188 | 439 | 2 428 |
| 12 | 1 081 | **422** | 2 289 |
| 16 | 947 | 468 | 2 044 |
| 20 | 863 | 512 | 1 874 |
| 64 | 615 | 547 | 1 392 |

**Il n'y a pas de plateau.** Sur les deux logos la reponse est strictement
decroissante. Ce qui ressemble a une falaise, de 8 174 a 1 419 entre 4 et 8 sur
`mark` 256 et de 11 368 a 3 033 entre 6 et 8 sur `mark` 1024, est une pente
forte mais continue, pas une discontinuite.

**Il reste une non-monotonie, mais elle est etroite.** `panel` descend a 422
noeuds a `-tol 12` puis **remonte** a 468, 512, 547. Soit 30 % de noeuds en plus
pour une tolerance 5 fois plus grande. C'est coherent avec
`budget = Tolerance * Reference / (Reference + gradient)` : le budget d'erreur
est par point, et Douglas-Peucker ne conserve pas ses points entre deux
tolerances, donc plus de tolerance peut garder plus de points. Le phenomene est
la sur de tres petites images a peu de bandes, ou l'erreur domine le compte.

**La consequence de methode, corrigee.** Le point de reglage du corpus,
`-tol 0,25`, n'est pas dans une zoneplate : il est dans la partie **douce** de la
courbe, ou le compte de noeuds bouge de 23 481 a 21 833 sur `mark` 256. Il est
donc un point sage, mais aussi un point ou le curseur ne fait presque rien. Le
plafond de noeuds ne mord vraiment qu'entre `-tol 4` et `-tol 8`, la ou la
courbe chute de 5,8 fois. C'est la que se joue D6, et c'est aussi la que la
boucle de budget doit atteindre, donc la ou elle travaille le plus.

### Le mecanisme de l'asymetrie : lineaire contre racine

J'ai longtemps note que le mecanisme etait inconnu, et c'etait vrai pour la
question posee — pourquoi la boucle de budget bridayait-elle si fort. La question
utile a une reponse simple, et elle tient en geometrie elementaire. Le meme logo
a quatre resolutions, rendu par inkscape avec `-w N -h N` sur le SVG d'origine,
`k=16`, `-tol 0,25`, `-curve-tol 0,4`, `-curve-simplify 1,5`, aucun plafond :

| echelle | lignes | courbes | x lignes | x courbes | ligne predite | courbe predite |
|--:|--:|--:|--:|--:|--:|--:|
| 256 | 21 833 | 966 | 1,00 | 1,00 | x1 | x1 |
| 512 | 43 610 | 1 354 | 2,00 | 1,40 | x2 | x1,41 |
| 1024 | 87 260 | 1 802 | 4,00 | 1,87 | x4 | x2,00 |
| 2048 | 171 852 | 2 705 | 7,87 | 2,80 | x8 | x2,83 |

Le chemin lignes est **lineaire** en resolution, au point pres : 1,00, 2,00, 4,00,
7,87 contre 1, 2, 4, 8. Ce n'est pas un defaut, c'est ce qu'emettre une marche
d'1 px veut dire. Le nombre de noeuds suit le perimetre divise par le pas de la
marche, et les deux grandissent ensemble, donc le quotient est invariant
d'echelle. Les points traces bruts le confirmaient deja : 23 925 a 256, 94 765 a
1024, soit 3,96x pour 4x, exactement comme le perimetre.

Le chemin courbe est **sous-lineaire**, en racine de la resolution sur ce logo :
1,00, 1,40, 1,87, 2,80 contre 1, 1,41, 2,00, 2,83, donc a moins de 5 %. La
justification est geometrique : un arc de rayon `R` ajuste a une tolerance `t`
demande `n` segments de l'ordre de `racette(R / 8t)`, donc `racette(R)` a `t`
fixe.

**Mais 0,5 n'est pas une loi, c'est une propriete de ce dessin.** Le test
`TestLineNodesScaleWithResolutionAndCurveNodesDoNot` mesure l'exposant sur un
disque et trouve 0,35, pas 0,5 : sur x16, 190 noeuds en lignes contre 21 en
courbes a x1, et 2 818 contre 56, donc exposants 0,97 et 0,35. Le test ne fige
donc que ce qui est solide, la ligne lineaire a 0,85 pres et la courbe sous 0,6
avec un ecart d'au moins 0,25, parce que c'est l'ecart qui fait croitre le
rapport. Figer 0,5 figerait une coincidence.

**Les deux lois ensemble disent pourquoi `-curves` vaut de plus en plus.** Le
rapport vaut `R / racine(R)`, donc il croit comme `racette(R)` : 22,6x a 256,
32,2x a 512, 48,4x a 1024, 63,5x a 2048, contre 22,6 x 1, 1,41, 2, 2,83 = 22,6,
31,9, 45,2, 63,9 de prediction. Le mode n'est pas « meilleur », il est
**asymptotique** : sur une image grande, une marche d'1 px coute des noeuds
proportionnels au perimetre, et un segment de Bezier n'en coute que la racine.
C'est la seule raison serieuse de le proposer.

Et cela referme la question de ma premiere hypothese, qui etait fausse dans sa
forme. Je proposais de faire suivre la resolution par `-curve-simplify` : le
mauvais bouton, dans la mauvaise direction. Ce n'est pas la tolerance du mode
courbe qui doit changer, c'est qu'a haute resolution le chemin lignes n'a
mathematiquement pas d'issue et qu'il faut changer de mode. La resolution-echelle
est une raison de choisir `-curves`, pas une correction a lui apporter.

### La boucle de budget ne gaspille rien

A `-max-nodes 4000`, les treize cas du corpus n'utilisent que **24 656 des
52 000 noeuds autorises, soit 47 %**, et aucun ne depasse :

| cas | noeuds | usage | cas | noeuds | usage |
|---|--:|--:|---|--:|--:|
| alpha/badge | 706 | 18 % | synthetic/diag-stripes | 2 214 | 55 % |
| icon/gear | 1 966 | 49 % | synthetic/diagonal-square | 3 263 | 82 % |
| lineart/glyph | 1 077 | 27 % | synthetic/diagonal-stroke | 3 203 | 80 % |
| logo/mark | 1 155 | 29 % | synthetic/tri-blade | 3 109 | 78 % |
| logo/mark-scaled | 2 325 | 58 % | ui/panel | 1 304 | 33 % |
| pixelart/blob | 56 | 1 % | synthetic/arrow | 1 475 | 37 % |
| synthetic/chevron | 2 803 | 70 % | | | |

J'ai lu ca comme du gaspillage et ecrit une variante de la boucle qui encadre
le budget puis bissectionne, pour depenser le budget disponible. **Elle ne
change rien** : hors `logo/mark` le premier essai a `-tol 0,25` est deja sous le
budget, donc la boucle n'est jamais atteinte, et sur `mark` elle n'a rien a
gagner. Le changement a ete reverté.

La lecture juste etait l'inverse. D6 est un **plafond**, pas une cible, et le
travail de l'encodeur est de minimiser la taille sous les contraintes. Ne pas
utiliser tout le budget n'est pas un defaut, c'est le but : chaque noeud non
employe est un noeud qui ne coute rien en octets. Ce que le tableau dit
vraiment, c'est qu'a `-tol 0,25` la fidelite est deja bonne et la taille
minimale, et que `-max-nodes` n'a aucun mordant sur les treize cas d'art plat,
sauf sur `logo/mark`, le seul a depasser 20 000 points au point de reglage. Sur
une photographie le plafond ne mord pas non plus, mais pour la raison inverse :
il ne peut pas etre tenu, voir la section sur la photographie.

### Trois erreurs de mesure de ma part

Elles se ressemblent et il faut les nommer, parce qu'elles ont produit des
fausses conclusions successives.

1. `make ci` ne reconstruit pas `bin/`. Une comparaison a rendu un resultat
   identique au bit pres, j'en ai conclu « neutre », et il n'y avait rien a
   changer : je mesurais un binaire perime. Des lors `make build` avant toute
   mesure.
2. Plus grave, et de la meme famille : j'ai balaye `-tol` **avec** `-max-nodes
   20000**. Le sujet mesure n'etait alors pas la reponse du simplifyeur mais la
   boucle de budget, qui escalade la tolerance jusqu'a passer sous le plafond.
   Le resultat est un tableau correct de la boucle, presente comme une
   propriete du curseur, avec un plateau et des inversions qui n'existent pas.
   Le tableau actuel, lui, est mesure sans plafond.
3. J'ai melange deux compteurs dans un meme ratio. La colonne `points` de
   `svgstat` est le pointage *aplati*, ce que verrait un rendu qui transforme
   chaque courbe en petits segments ; ce n'est pas le nombre de noeuds emis, qui
   est la grandeur de D6. Sur un path courbe les deux different de 1,7x a 2,1x
   selon le dessin, assez pour transformer 15,6x en 10,2x sans qu'aucune ligne
   de calcul ne soit fausse. Un ratio lignes/courbes ne se prend que sur la
   colonne `nodes` de `svgimg`, celle que `-max-nodes` mesure.

Les trois ont la meme cause : croire la sortie et la reconstruire. Le
controle qui manque est de relire ce que la commande a reellement imprime —
`res.Tolerance` dit ici 5,2525 px pour une tolerance demandee de 0,15, ce qui
 aurait du etre le signal depuis le debut.

### Combien `-curves` vaut, et de quoi ça dépend

La question « `-curves` vaut-il 24× ou 5,9× ? » n'a pas de réponse sans dire le
plafond. Le chemin courbé produit **4 199 nœuds quelle que soit la limite**,
parce qu'il n'approche jamais un plafond serré ; le chemin lignes, lui, est
dégradé de plus en plus à mesure que la limite descend. Treize cas **d'art plat**
(les photos sont hors de ce tableau, voir plus bas), `k=16`, `-tol 0,25`,
`-curve-tol 0,4` (le défaut de la CLI) :

| plafond | lignes | courbes | ratio | cas où la boucle a escaladé |
|--:|--:|--:|--:|--:|
| 500 | 4 295 | 4 199 | **1,0×** | 12 sur 13 |
| 1 000 | 7 764 | 4 199 | 1,8× | 11 sur 13 |
| 2 000 | 11 388 | 4 199 | 2,7× | 8 sur 13 |
| 4 000 | 24 656 | 4 199 | 5,9× | 6 sur 13 |
| 8 000 | 46 595 | 4 199 | 11,1× | 5 sur 13 |
| 20 000 | 65 431 | 4 199 | 15,6× | 2 sur 13 |
| aucun | 153 925 | 4 199 | 36,7× | 0 sur 13 |

Donc le gain de `-curves` est **une fonction du plafond, et rien d'autre**. Il
n'est pas une compression générale : c'est un mode qui compte lorsqu'on a de la
place dans le budget de nœuds. À 500 nœuds par cas, les deux modes rendent
4 295 et 4 199 nœuds, et c'est le plafond qui décide, pas le mode : le chemin
courbé n'a plus rien à apporter parce qu'il ne peut plus être simplifié
davantage, et le chemin lignes a été désarmé jusqu'au même compte.

C'est la réponse à la question de la Suite, et elle n'est pas « oui ou non » :
`-curves` est pertinent à D6 = 20 000 où il rend 15,6×, il est encore utile à
4 000 où il rend 5,9×, et il est **inert en dessous de 1 000**. Le coût reste le
même dans les trois régimes, environ +0,008 à +0,010 de RMSE sur ces treize
cas. L'intérêt du mode dépend donc du budget que le programme s'autorise, et
cette question-là n'est pas technique ; le défaut du projet, lui, est tranché
plus bas.

Les deux protocoles sont donc compléments, pas concurrents. Sans plafond, sur le
compteur de D6 — les nœuds émis : **29,3×** sur 169 538 → 5 781 pour les trente et
un cas hors photographie, Δscore moyen +0,0193, pire cas +0,0596 sur
`lineart/wind`, meilleur **−0,0147** sur `icon/vector-pen`, qui s'améliore donc
en passant aux courbes. Avec un plafond de 4 000 sur les treize cas d'origine :
5,9× sur 24 656 → 4 199, Δscore moyen +0,0079 ; sur les trente-trois cas le même
plafond donne 3,1×, mais les photographies ne peuvent pas être bridées — le
chemin lignes y consomme 142 % du budget — donc ce chiffre n'est pas une
propriété du mode. Le protocole bridé flatte la ligne, parce qu'il la dégrade,
donc il sous-estime le mode. Le protocole libre est le bon pour « combien de
nœuds coûte ce mode », le bridé est le bon pour « à budget fixé, qui est le plus
fidèle ».

Le 24,0× qui a circulé un moment venait du compteur de `svgstat`, dont la colonne
`points` est le pointage *aplati* et non les nœuds émis : sur un path courbé elle
donne 1,7× à 2,1× plus de points que de nœuds, et le facteur dépend du dessin.
Les deux compteurs ne sont donc pas comparables, et seule la colonne `nodes` de
`svgimg` — celle que `-max-nodes` mesure — peut porter un ratio lignes/courbes.
`make corpus-bench` nomme désormais la sienne `points (aplatis)` pour que la
confusion ne se refasse pas.

#### Décision : défaut lignes, documentation par la courbe

Le défaut reste le chemin lignes, et le mode ne s'active pas tout seul quand un
plafond est posé. Un seuil d'activation serait une fausse précision : le rapport
est continu en fonction du plafond (1,0× à 500, 5,9× à 4 000, 15,6× à 20 000) et
dépend du contenu, donc aucun nombre unique ne dit « activez ici ». Ce qu'on peut
dire sans se tromper, c'est que **sous 1 000 nœuds le mode est inerte** et qu'il
commence à payer vers 4 000. La documentation montre donc la courbe et laisse
l'appelant choisir son budget ; les photographies, elles, restent hors de portée
quel que soit le plafond.

L'activation automatique aurait un second défaut : elle changerait la fidélité
sans le dire, au moment précis où la boucle de budget escalade déjà la tolérance.
Le mode reste un opt-in explicite, et `-max-nodes 0` (le défaut) est le régime où
il ne faut pas l'activer.

### La photographie change le chiffre, et casse le plafond

Les trente et un cas hors photographie donnent 29,3× sans plafond. Les deux
photographies, à tolérance égale et sans plafond, donnent :

| cas | lignes | courbes | ratio | Δscore |
|---|--:|--:|--:|--:|
| `photo/des` 800×600 | 157 014 | 36 829 | 4,3× | +0,0061 |
| `photo/tux` 386×395 | 99 206 | 18 363 | 5,4× | +0,0174 |

Le corpus complet, trente-trois cas : 425 758 → 60 973, soit **7,0×**. Le 36,7×
des treize cas d'origine n'était pas faux, il décrivait le sous-ensemble le plus
favorable : sans photographie et avec six dessins synthétiques taillés pour lui.
Il ne faut pas le citer comme le gain du mode.

Plus grave pour D6 : **le plafond de nœuds ne peut pas mordre sur une photo.**
À `-max-nodes 4000`, `des.png` escalade la tolérance de 0,25 à **1 838,97** — le
domaine OKLab entier vaut 1,0 — et rend quand même **114 218** nœuds, vingt-huit
fois le plafond ; `tux.png` monte à 263,50 et rend 32 464. La cause n'est pas
l'échantillonnage des bords mais le **nombre de régions** : quantifier une photo
en 16 couleurs laisse 7 806 boucles à tracer, et chaque boucle coûte des nœuds
quoi qu'on fasse de la tolérance. Le chemin courbé ne fait pas mieux (36 829 sur
`des`), il est seulement trois fois plus économe.

La bibliothèque ne cache pas l'échec : elle affiche `status OVER the node budget`
et une note qui donne le compte et la tolérance atteinte. C'est le bon
comportement — un plafond qu'on ne peut pas tenir doit être signalé, pas obtenu
en dégradant l'image jusqu'à l'absurde.

## Suite

1. ~~Documenter `-curves` par sa courbe, pas par un oui ou non.~~ **Fait, décision
   prise** : le mode rend 15,6× à un plafond de 20 000 nœuds, 5,9× à 4 000 et 1,0×
   à 500, parce que le chemin courbé rend toujours 4 199 nœuds et que la ligne est
   dégradée jusqu'au plafond. Il reste désactivé par défaut, sans activation
   automatique : le rapport est continu en fonction du plafond, donc un seuil
   serait une fausse précision ; il est inerte sous 1 000 nœuds et commence à
   payer vers 4 000, pour +0,0193 de RMSE en moyenne sur les 31 cas hors photo
   (pire +0,0596). Le corpus reste mince sur le contenu : six des trente-trois cas
   sont synthétiques, et les deux photographies sont la borne dure.
2. ~~Un test qui verifie les lois d'echelle.~~ **Fait** :
   `TestLineNodesScaleWithResolutionAndCurveNodesDoNot`, qui synthetise ses
   rasters, ne depend ni d'inkscape ni d'un image de plus, et ne fige que
   l'ecart entre les exposants, pas l'exposant lui-meme.
3. ~~Etendre le corpus vers les 30 a 60 cas prevus.~~ **Fait, et `prune` est
   tranche** : 33 cas, dont 18 icones tierces MIT et 2 photographies ; zero
   fusion de `prune` sur les 33 cas comme sur un degrade synthetique, donc il a
   ete supprime. Le corpus est a la borne basse de la fourchette ; l'etendre
   encore donnerait a `ui/panel` le poids d'un cas reel et non d'un seul exemple.
4. Puis seulement : grain / tramage, et l'axe video de D1.
