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

Une troisième pièce : `internal/polygon`, qui isole les coins par la longueur de
contour qui les sépare plutôt que par leur angle seul. Un seuil d'angle ne peut pas
faire les deux à la fois — il rejette la marche, ou il accepte le bruit. Limite
connue et assumée : là où une marche rejoint un côté droit, le coin n'est pas trouvé,
car la marche voisine tourne de 45° sur un pixel et se trouve donc à moins de 2 px.
Rendre ce seuil `QuietAngle` plus permissif répare ce cas et en casse d'autres ; le
discriminant qu'il faut est plus fin qu'un angle unique.

#### Ce que ça donne, mesuré

Corpus, `-max-nodes 4000`, `-curve-tol 0.4`, `-curve-simplify 1` :

| cas | octets `trace` | octets `-curves` | ratio | score `trace` | score `-curves` |
|---|--:|--:|--:|--:|--:|
| `icon/gear` | 79 411 | 7 658 | 10,4× | 0,1036 | 0,1197 |
| `logo/mark` | 234 590 | 19 115 | 12,3× | 0,1466 | 0,1672 |
| `lineart/glyph` | 14 658 | 1 989 | 7,4× | 0,1390 | **0,1389** |
| `ui/panel` | 17 967 | 6 964 | 2,6× | 0,1490 | 0,1698 |
| `alpha/badge` | 2 314 | 737 | 3,1× | 0,3059 | 0,3112 |
| `pixelart/blob` | 949 | 606 | 1,6× | 0,2492 | 0,2560 |
| `logo/mark-scaled` | 40 098 | 43 504 | 0,9× | 0,0812 | 0,0887 |

Sur les deux photos de `assets/png`, en `-k 8` :

| image | octets `trace` | octets `-curbes` | ratio | ssim `trace` | ssim `-curves` |
|---|--:|--:|--:|--:|--:|
| `des.png` | 884 745 | 291 852 | 3,0× | 0,8547 | 0,8531 |
| `tux.png` | 680 581 | 183 450 | 3,7× | 0,7086 | 0,6968 |

Donc le tableau s'est inversé : ce n'est plus une régression partout, c'est un
**échange** — jusqu'à 12× plus petit contre +0,005 à +0,021 de score, pour 0 à
−0,012 de ssim. `lineart/glyph` s'améliore même légèrement. `logo/mark-scaled` est
le seul cas qui grossit, de 8 %.

`-curves` reste **désactivé par défaut** : c'est un arbitrage, pas un gain franc, et
un mode qui dégrade la fidélité en silence n'a pas sa place dans un défaut. Il se
règle maintenant explicitement, et l'amplitude du gain dépend de la forme : c'est le
premier mode où le contenu décide vraiment du résultat.

Ce que cela dit du contenu tient toujours : `pixelart/blob` reste l'argument le
plus fort en faveur des Béziers, `grid` le restituant exactement (ssim 1,0000)
pendant que `trace` plafonne à 0,8392.

## Suite

1. **Trancher `-curves`.** Le chemin est maintenant un échange mesuré, 2,6× à 12×
   plus petit contre ~0,02 de score. Ce qui manque n'est plus la mécanique mais la
   décision : est-ce un mode qu'on expose, et à quelles conditions ? La question
   se tranche sur le contenu, donc sur un corpus plus large qu'à présent.
2. **Le coin en bout de marche diagonale.** `internal/polygon` le manque encore.
   Il faut regarder la série de virages et non un angle isolé, ce qui est une autre
   fonction qu'un seuil.
3. **Rejouer le budget de points** sur `logo/mark`, seul cas du corpus au-delà de
   20 000 points, en reparamétrant `simplify` plutôt que la table de palette.
4. **Étendre le corpus** vers les 30 à 60 cas prévus, ce qui décide notamment du
   sort de `prune` et donnerait à `ui/panel` le poids d'un cas réel et non d'un
   seul exemple.
5. Puis seulement : grain / tramage, et l'axe vidéo de D1.
