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
elles ne sont pas la cible. Sans corpus, le traceur était évalué sur la seule
chose où il est structurellement mauvais, et chaque décision prise sur cette base
—allocation de palette, tolérance, politique de coins — était une extrapolation
non testée.

Le corpus est dans `testdata/corpus/<categorie>/<nom>.svg` + `<nom>.png`, avec
`make corpus-render` pour régénérer les rasters depuis les SVG. Les SVG sont
écrits à la main et font foi : le raster est **dérivé** du SVG, pas collecté, donc
un désaccord entre les deux est un bug du harness et non une tolérance à
documenter. Vérifié : `embed` rejoue chaque raster à l'identique (AE=0), sauf
`alpha/badge` — attendu, `embed` ne porte qu'un seul rectangle, et c'est
précisément le cas que `trace` doit rendre via `fill-opacity`.

| cas | ce qu'il teste |
|---|---|
| `logo/mark` | logo plat : anneau, rectangle, triangle acutangle, cercles |
| `logo/mark-scaled` | le même logo rendu à 1024 px, pour exposer le chanfrein à l'échelle |
| `icon/gear` | 8 dents à angles droits, un trou |
| `ui/panel` | la cible v1 : rectangles, filet 1 px, coins arrondis |
| `lineart/glyph` | traits minces à fort contraste |
| `pixelart/blob` | aligné sur la grille de pixels, aucun antialiasing |
| `alpha/badge` | semi-transparence : 53 % de l'image en alpha nul |

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

`K` devient donc un majorant et non une cible, et `Palette.Merged` rend le nombre
de slots inutilisés observable au lieu de le laisser deviner.

### `prune` ne fait rien, et c'est mesuré

Un mécanisme de fusion par distance de couleur a été ajouté sur le même
problème : après la coupe, deux boîtes dont les couleurs sont indiscernables
plutôt que redondantes sont absorbées l'une dans l'autre. Il est resté **inactif
sur les sept cas du corpus et sur les deux photos** — `Merged = 0` partout, avec
`MergeDelta` à 0,02 comme à 0.

Les deux mécanismes se ressemblent mais ne visent pas le même instant : le plancher
.refuse de *créer* une petite boîte, `prune` supprime une petite boîte qui a
survécu. Et ces survivantes existent bel et bien — la médiane coupe à mi-chemin,
donc un enfant peut tomber sous le plancher alors que son parent était au-dessus :
une boîte de 48 pixels issue d'un parent de 1000 est produite ainsi. La question
n'est donc pas « le plancher a-t-il fonctionné » mais « la palette a-t-elle déjà
produit des doublons ». Sur ce corpus, non.

Il est **gardé quand même**, pour une raison qui tient en un mot : le corpus. Il
compte 7 cas là où le plan en prévoit 30 à 60, et « je ne l'ai pas vu se déclencher
sept fois » n'est pas « il ne se déclenchera jamais ». Le garder coûte quarante
lignes et un champ de plus ; le supprimer laisserait le budget de palette sans
filet si une image hors corpus produisait enfin un doublon, et l'appelant n'aurait
plus rien pour le rattraper.

Il est donc documenté comme **filet non exercé**, explicitement, pour que personne
ne lise le code et suppose qu'il porte une part du résultat. C'est le seul endroit
du projet où un mécanisme est conservé sur une probabilité et non sur une mesure,
et cela mérite d'être dit en toutes lettres.

Deux bugs d'implémentation corrigés au passage, tous deux trouvés par la mesure :

- `append` en place dans `prune` **écrasait** la boîte voisine. `bisect` renvoie
  `b[:h]` et `b[h:]`, deux vues du même tableau, donc absorber une boîte dans une
  autre détruisait la troisième. Symptôme : le carré rouge du badge virait au
  bleu marine. `TestMergeDoesNotCorruptAdjacentBoxes` épingle l'invariant.
- Un seuil fondé sur l'aire, essayé avant la distance de couleur, **volait le
  travail de `MinArea`**: un point blanc d'un pixel sur 40×20 fait 0,125 % de
  l'image, il était absorbé dans le noir au niveau de la palette et `MinArea`
  n'avait plus de région à supprimer. Aucune règle d'aire ne peut distinguer une
  marque d'un pixel d'un artefact d'un pixel : ils pèsent le même. La distance de
  couleur les sépare sans savoir lequel est lequel, parce qu'un artefact
  d'antialiasing est par définition très proche de la couleur qu'il borde, tandis
  qu'une marque a une couleur propre.

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
fixé à 0,4 :

| `-curve-simplify` | nœuds (13 cas) | ratio | Δscore moyen | Δssim moyen | pire Δssim |
|---|--:|--:|--:|--:|--:|
| 0,75 | 18 378 | 3,6× | +0,0121 | −0,0092 | −0,0319 |
| 1 | 9 989 | 6,6× | +0,0102 | −0,0074 | −0,0274 |
| **1,5** | **6 435** | **10,2×** | **+0,0103** | **−0,0083** | **−0,0263** |
| 2 | 5 746 | 11,4× | +0,0162 | −0,0127 | −0,0556 |
| 3 | 5 145 | 12,8× | +0,0196 | −0,0146 | −0,0569 |

**1 est dominé par 1,5** : même coût de fidélité, 10,2× au lieu de 6,6×. Le défaut
passe donc de 1 à 1,5. En dessous, le score monte plus vite que les nœuds ne
baissent ; au-dessus, plus vite encore.

Il n'existe **aucun réglage gratuit**. 4 cas sur 13 ne perdent rien à un réglage
quelconque, les 9 autres paient. À 1,5, en nœuds :

| cas | `trace` | `-curves` | ratio | Δscore | Δssim |
|---|--:|--:|--:|--:|--:|
| `synthetic/diagonal-stroke` | 9 916 | 145 | **68,4×** | +0,0177 | −0,0092 |
| `synthetic/diagonal-square` | 9 278 | 165 | **56,2×** | +0,0041 | −0,0014 |
| `synthetic/tri-blade` | 9 664 | 187 | **51,7×** | +0,0076 | −0,0025 |
| `synthetic/chevron` | 2 809 | 64 | 43,9× | +0,0268 | −0,0174 |
| `synthetic/arrow` | 1 480 | 38 | 38,9× | **−0,0001** | +0,0010 |
| `synthetic/diag-stripes` | 2 226 | 108 | 20,6× | +0,0093 | −0,0043 |
| `icon/gear` | 6 426 | 571 | 11,3× | +0,0174 | −0,0263 |
| `logo/mark` | 17 827 | 1 614 | 11,0× | +0,0287 | −0,0217 |
| `lineart/glyph` | 1 089 | 168 | 6,5× | +0,0009 | −0,0017 |
| `alpha/badge` | 714 | 141 | 5,1× | +0,0019 | −0,0098 |
| `ui/panel` | 1 365 | 573 | 2,4× | +0,0058 | −0,0021 |
| `pixelart/blob` | 58 | 27 | 2,1× | +0,0068 | −0,0113 |
| `logo/mark-scaled` | 2 897 | 2 634 | **1,1×** | +0,0071 | −0,0006 |

**Ce tableau là n'est pas équitable, et il est remplacé plus bas.** Les deux modes
recevaient `-max-nodes 20000` dans `corpus-bench`, donc le mode lignes était
bridé au plafond pendant que le mode courbé, qui n'approche jamais 20 000, ne
l'était pas. Sur `logo/mark` la boucle a escaladé la tolérance de 0,25 à 1,0
pour passer sous le plafond, et c'est ce 17 827 qui est comparé au 1 614 du mode
courbé. Le 1,1× de `logo/mark-scaled` est le même artefact, dans l'autre sens :
là, c'est le 1024 qui a été bridé et réduit à 2 897.

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
va de 1× à 68× pour environ 0,01 de RMSE OKLab. `-curves` reste **désactivé par
défaut** tant que la décision n'est pas prise explicitement, mais ce n'est plus
« ne pas activer » par prudence, c'est « activer ou non, sur des chiffres ».

Sur les deux photos de `assets/png`, en `-k 8` :

| image | octets `trace` | octets `-curbes` | ratio | ssim `trace` | ssim `-curves` |
|---|--:|--:|--:|--:|--:|
| `des.png` | 884 745 | 291 852 | 3,0× | 0,8547 | 0,8531 |
| `tux.png` | 680 581 | 183 450 | 3,7× | 0,7086 | 0,6968 |

Donc le tableau s'est inversé : ce n'est plus une régression partout, c'est un
**échange** — jusqu'à 12× moins de nœuds contre +0,005 à +0,028 de score, pour 0
à −0,012 de ssim. `lineart/glyph` et `alpha/badge` s'améliorent même légèrement.
`logo/mark-scaled` est le seul cas qui grossit, de 7 %.

`-curves` reste **désactivé par défaut** : c'est un arbitrage, pas un gain franc, et
un mode qui dégrade la fidélité en silence n'a pas sa place dans un défaut. Il se
règle maintenant explicitement, et l'amplitude du gain dépend de la forme : c'est le
premier mode où le contenu décide vraiment du résultat.

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
minimale, et que `-max-nodes` n'a aucun mordant sur ce corpus, sauf sur
`logo/mark`, le seul cas a depasser 20 000 points au point de reglage.

### Deux erreurs de mesure de ma part

Elles se ressemblent et il faut les nommer, parce qu'elles ont produit deux
fausses conclusions_successives.

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

Les deux ont la meme cause : croire la sortie et la reconstruire. Le
controle qui manque est de relire ce que la commande a reellement imprime —
`res.Tolerance` dit ici 5,2525 px pour une tolerance demandee de 0,15, ce qui
 aurait du etre le signal depuis le debut.

### Combien `-curves` vaut, et de quoi ça dépend

La question « `-curves` vaut-il 24× ou 5,9× ? » n'a pas de réponse sans dire le
plafond. Le chemin courbé produit **4 199 nœuds quelle que soit la limite**,
parce qu'il n'approche jamais un plafond serré ; le chemin lignes, lui, est
dégradé de plus en plus à mesure que la limite descend. Treize cas, `k=16`,
`-tol 0,25` :

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
même dans les trois cas, environ +0,008 à +0,010 de RMSE. La décision dépend
donc du budget que le programme s'autorise, et cette question-là n'est pas
technique.

Les deux protocoles sont donc complements, pas concurrents. Sans plafond : 24,0×
sur 154 243 → 6 435 nœuds, Δscore moyen +0,0103, pire cas +0,0274 sur
`logo/mark`, meilleur −0,0001 sur `synthetic/arrow`, donc du bruit. Avec un
plafond de 4 000 : 5,9× sur 24 656 → 4 199, Δscore moyen +0,0079. Le protocole
bridé flatte la ligne, parce qu'il la dégrade, donc il sous-estime le mode. Le
protocole libre est le bon pour « combien de nœuds coûte ce mode », le bridé est
le bon pour « à budget fixé, qui est le plus fidèle ».

## Suite

1. **Documenter `-curves` par sa courbe, pas par un oui ou non.** La question est
   tranchee : le mode rend 15,6× a un plafond de 20 000 noeuds, 5,9× a 4 000, et
   1,0× a 500, parce que le chemin courbe rend toujours 4 199 noeuds et que la
   ligne, elle, est degradee jusqu'au plafond. Ce n'est pas une compression
   generale, c'est un mode qui compte quand le budget de noeuds est large. Le cout
   est constant, environ 0,01 de RMSE. Reste a decider si le defaut reste le chemin
   lignes et si le mode se documente par cette courbe ou par un seuil, et a quel
   seuil. Le corpus est toujours mince sur le contenu, six des treize cas etant
   ecrits pour le test.
2. **Un test qui compare deux rasters a deux echelles.** Le comportement du
   traceur depend de l'echelle de rasterisation, pas seulement de la forme, et
   rien ne le verifie. Ce qui est etabli : le chemin lignes paie la resolution au
   proportionnel, 4,0× de noeuds pour 4×, et le chemin courbe non, 1,87×. Ce qui
   ne l'est pas : pourquoi. Les deux hypotheses testees sont refutees, la rampe
   d'antialiasing et la tolerance proportionnelle. Un test qui figerait le
   comportement non explique risquerait de figer un defaut.
3. **Etendre le corpus** vers les 30 a 60 cas prevus, ce qui decide notamment du
   sort de `prune` et donnerait a `ui/panel` le poids d'un cas reel et non d'un
   seul exemple. C'est le seul point de la Suite qui demande du travail humain
   et non une mesure de plus.
4. Puis seulement : grain / tramage, et l'axe video de D1.
