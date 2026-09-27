# SVG : format, mosaïque vectorielle, et pourquoi la vidéo change tout

Document de cadrage. Aucune décision figée ici : c'est le support de la discussion.

---

## 1. La thèse qu'on doit valider avant d'écrire une ligne

Transformer une image binaire en SVG **n'est pas un problème de conversion de format**.
C'est un problème de **vectorisation** : on cherche une description géométrique
sparse et continue d'un signal dense et discret. Il y a une perte d'information
inévitable, et la seule question qui compte est :

> **quelle métrique d'erreur on minimise, et quel domaine cible on s'autorise ?**

La réponse naïve (« minimiser l'erreur pixel ») produit un monstre. Un JPEG 8×8 KB
devient un SVG de 3 MB qui rend *moins bien*. C'est exactement le piège dans lequel
sont tombés la plupart des convertisseurs des années 2000.

Donc la thèse à valider n'est pas « on peut vectoriser n'importe quoi » mais :

> **La valeur de la vectorisation n'est pas la fidélité, c'est l'éditabilité.**
> Reconnaître 40 formes au lieu de 65 536 pixels, les rendre recolorables,
> re-stylables, animables, accessibles, textuelles et diffables en VCS.
> On ne vise pas la photoréalité, on vise une **fidélité stylisée** et **bornée**.

Si on ne tient pas cette ligne, le projet est mort-né : il sera toujours perdant
contre `<image href="photo.png"/>`.

### 1.1 L'hypothèse nulle, à garder visible pour toujours

La seule baseline qui compte n'est pas « les autres vectoriseurs », c'est :

```xml
<svg viewBox="0 0 W H" width="W" height="H">
  <image href="data:image/png;base64,..." width="W" height="H"/>
</svg>
```

Zéro perte, ~33 % de surcoût en base64, et **c'est déjà du SVG valide**.

**Décision D6 : la taille en octets n'est pas un objectif.** Le transport
gzip/brotli riddle un SVG de 5 à 10×, donc les octets sont essentiellement un
problème d'empaquetage, résolu tard et sans consequence sur l'algorithme.

Conséquence à assumer explicitement : sans objectif de taille, cette baseline
devient **objectivement très forte** — un seul nœud DOM, fidélité parfaite,
transport correct. Le seul arbitrage qui reste est *« a-t-on besoin de
l'éditer ? »*. Ce n'est pas un problème pour le projet, c'est **sa thèse** :
le vecteur ne se justifie que là où l'éditabilité, l'accessibilité et la
scalabilité ont une valeur, donc le domaine cible est le contenu graphique
structuré, pas la photographie. Une baseline qui gagne quelque part n'est pas
une gêne, c'est la frontière du projet.

Ce qui ne peut **pas** être traité comme de la compression, en revanche, c'est
le **nombre de nœuds** : c'est la seule chose que le moteur de rendu manipule
à chaque frame, et c'est une contrainte physique, pas une préférence. Voir §10.

Tout algorithme qu'on écrit doit donc battre cette baseline sur un axe :
nombres de nœuds, éditabilité, accessibilité, fidélité. Jamais « en moyenne ».

Conséquence organisationnelle : `svgstat` et le harness de rendu comparatif
(Phase 0) sont l'infrastructure la plus importante du projet, **avant**
le premier algorithme.

---

## 2. Anatomie du problème

```
image binaire (H·W·{1,3,4} octets, dense, discrétisé)
        │
        │  ← décomposition : où sont les frontières ?
        ▼
champ scalaire continu (luminance, magnitude de gradient, distance signée)
        │
        │  ← extraction de level-set, interpolation sous-pixel
        ▼
contours (polygones fermés + trous, sous-pixel, topologie correcte)
        │
        │  ← simplification + fitting de courbes
        ▼
primitives géométriques (paths,fills, grouping)
        │
        │  ← encodage texte (le coût est ici, pas avant)
        ▼
document SVG (arbre, sérialisé)
```

Quatre étapes distinctes, dans cet ordre. Les confondre est l'erreur d'architecture
la plus fréquente :

| # | Transformation | Nature |(non invertible) |
|---|----------------|--------|------------------|
| 1 | décomposition couleur/structure | information → information, **perte intentionnelle** | non |
| 2 | champs → level-sets | signal → géométrie, **sous-pixel** | oui (d'où le suréchantillonnage) |
| 3 | géométrie → segments | discrétisation | oui |
| 4 | géométrie → texte | quantification | non (exact) |

Les étapes 1–3 sont où se joue la qualité. L'étape 4 est arithmétique et optimisable
mécaniquement. **Ne pas passer 80 % de son temps sur l'étape 4.**

---

## 3. Le spectre des stratégies de vectorisation

Il existe un continuum complet, du trivial aucrew. Le point clé : chaque niveau
apporte un gain **non linéaire** en taille.

### A. L'encapsulation
`<image href="data:...">`. Perte nulle, gain nul. C'est la baseline (§1.1).
Utile pour : refus gracieux, et le mode « photo » (§8).

### B. La mosaïque rectangulaire
Découpe en tuiles, une tuile = un `<rect>` plein. Équivalent à une image
indexée. SVG n'apporte rien, le DOM explose.
Utile pour : un **étage intermédiaire** de debug, et éventuellement un encodage
de masque très grossier pour de l'alpha.

### C. Le run-length par lignes
Chaque ligne de pixels devient des `<rect>` horizontaux. Gagne sur la cohérence
horizontale, s'effondre sur le bruit et les photos.
Utile pour : scan art, images synthétiques à plateaux.

### D. Le tracé de contours — **le point sweet spot**
1. quantifier la couleur (k couleurs, **sans tramage**)
2. pour chaque index de palette : un masque binaire
3. tracer les contours avec interpolation sous-pixel
4. chaîner les boucles, réparer la topologie, imbriquer les trous
5. simplifier (Douglas-Peucker)
6. fitter des courbes (quadratiques ou cubiques)
7. émettre

Produit de **vraies formes éditables**. C'est ce que font potrace, autotrace,
Adobe Image Trace, Vectr. C'est le cœur du v1.

### E. La décomposition en rectangles
On ne trace pas des contours : on pave les régions avec des rectangles
maximaux (greedy meshing, max-rect). Beaucoup **moins** d'éléments que D, et
les éléments sont *sémantiques* (un bouton, un champ, une barre).
Sous-estimé, et c'est le bon mode pour les **captures d'écran / UI / diagrammes**.

### F. Le patch-based (photos → SVG « 2.5D »)
Empile des formes semi-transparentes pour approcher une photo. SVG devient un
moteur de rendu 2.5D. Précédent : Zhang et al. *Simplified Vector Graphic* (2016),
puis la famille d'optimisation avec rasteriseur différentiable (**DiffVG**,
2020) et génération par diffusion (**VectorFusion**, 2023).
- intérêt réel pour les logos/visuels de marque avec dégradés
- coût : orders de grandeur plus lent, et il faut écrire un rasteriseur
  différentiable en Go (couverture analytique + antialiasing + alpha,
 _backprop_ géométrique). Semaines, pas jours.

### G. Le neural (seq2seq / diffusion)
`DeepSVG`, `LIVE`, `IconShop`, `SVGDreamer`, `CLIPasso`. Sort des paths
*cohérents stylistiquement* plutôt que fidèles. Excellent pour de l'iconographie,
mauvais pour la fidélité, et lourd (Go = pas le bon langage, il faudrait
un service Python à côté). **Hors périmètre v1, mais c'est la direction
stratégique si le projet a du succès** — voir §13.

### Le bon encodage de cette èchelle est *le* livrable du projet

| Entrée | Cible réaliste | Méthode | Statut |
|---|---|---|---|
| logo, icône, UI plate | quasi-sans-perte, minuscule, éditable | D ou E | v1 |
| pixel art | exacte, palette dure, bords francs | D mode NN | v1.5 |
| screenshot, diagramme, graphe | haute fidélité, sémantique, greppable | E + détection de texte | v1.5 |
| dessin au trait, croquis, BD | traits faithfuls | **centreline**, pas contour (§7) | v2 |
| photo | affiche abstraite, ou **refus** | F, ou A + avertissement | v3 / refus |
| vidéo | animation plate, screen recording, motion design | espace-temps (§11) | v4 |

---

## 4. Le pipeline canonique (détail du cœur)

### 4.1 Décodage et représentation interne

- `image/jpeg`, `image/png` du stdlib ; `golang.org/x/image` pour webp/tiff/bmp.
- **Conversion immédiate vers un buffer planaire** (`[]uint8` ou `[]float32` par
  canal, ou interleavé). `image.Image.At()` est lent et l'implémentation n'est
  pas garantie thread-safe en lecture concurrente. On convertit **une fois**,
  ensuite on ne manipule plus que des slices.
- Espace colorimétrique : conversion **sRGB → linéaire → OKLab** une fois, et on
  travaille là. Raison : le sRGB n'est pas uniforme, donc une distance euclidienne
  en RGB surpondère bleu/vert ; en plus toutes les autres étapes (quantification,
  simplification, fitting) deviennent *interprétables* en termes perceptifs.

### 4.2 Quantification couleur

- Median cut (Heckbert 1980), k-means, ou Wu (1991). **Pondérer les distances en OKLab.**
  Un median-cut en sRGB naïf est visiblement faux sur les bleus saturés.
- `k` est **le premier levier de taille** de tout le projet.
- **Pas de tramage.** C'est contre-intuitif et c'est capital : le tramage
  (Floyd-Steinberg) crée du bruit haute fréquence, et le bruit vectorise
  **catastrophiquement** (des milliers de micro-contours). L'ordre correct est
  : *quantifier plat → tracer*. L'inverse est un désastre.
- Un article du blog de la lib : « nous ne tramons pas, voici pourquoi ».

### 4.3 Champs et level-sets — **où se gagne la qualité**

C'est le point le plus sous-estimé du domaine.

Tracer les marches d'une image binaire 1× donne un vecteur 1× : chaque bord est
une marche d'escalier, l'erreur de Hausdorff est O(1) pixel, quel que soit le
nombre de points. On ne gagne **rien** en augmentant le nombre de points.

La bonne approche : ne pas tracer l'image, tracer **le level-set d'un champ lissé**.

```
pixels → (lissage anisotrope) → (gradient) → magnitude |g|
      → seuillage + interpolation  → level-set {|g| = t}
      → marching squares avec décision asymptotique
```

- **Lissage anisotrope de Perona-Malik (1990)** plutôt qu'un flou gaussien :
  il lisse *dans* les régions plates et préserve *les* bords. Un gaussien arrondit
  les coins, un anisotrope ne les bouge pas.
- **Marching squares avec interpolateur linéaire** donne des contours sous-pixel
  gratuitement.
- **Décision asymptotique** (Nielson & Hamann, 1985) pour les cas de selle
  ambigus : sans elle, la topologie est occasionally fausse. Rare, mais ça crée
  des trous fantômes qu'on ne voit qu'à l'œil.
- La sortie est l'ensemble des level-sets de `{|g| > t}` — donc typiquement deux
 level-sets (extérieur/intérieur), ce qui donne directement **la bonne couleur :
  le côté le plus sombre**.

### 4.4 Topologie

- Chaînage des arêtes en boucles (8-connexité, type Suzuki & Abe 1985) : les
  coins « ambiguïté diagonale » d'un masque 8-connexé ont deux représentations
  possibles, il faut une règle fixe et documentée.
- Imbrication des trous : parent d'un contour = le contour qui le contient
  (even-odd vs. non-zero, on choisit *et on s'y tient*).
- Normalisation d'orientation : contours extérieurs sens horaire, trous sens
  antihoraire → règle de remplissage non-even-odd (`nonzero`) sans
  configuration du `fill-rule`.
- Nettoyage : composants de < N px², îlots, contours non fermés.

### 4.5 Simplification — pondérée par le gradient local

Douglas-Peucker (1973) avec une tolérance **globale** est la version faible.
La version qui marche : **tolérance locale inversement proportionnelle à la
structure locale**.

> Dans un aplat, on peut simplifier à 4 px sans dommage visible.
> Au bord d'un détail de 2 px, on ne peut pas simplifier du tout.

Donc : `ε(p) = ε_base · k / (1 + α·|∇I(p)|)`, ou en pratique une version à
deux régimes. C'est un petit changement de code pour un gain visuel disproportionné.

Puis : **suppression des points colinéaires** (indispensable avant le fitting,
DP laisse des alignsements) et suppression des cuspides.

### 4.6 Fitting de courbes

Deux algorithmes, un choix à faire explicitement :

- **Schneider (1990, Graphics Gems)** : fit de cubiques par moindres carrés avec
  reparamétrage de l'arc. Robuste, résout un système linéaire à chaque segment.
- **Selinger (2012)** : split récursif glouton, ajustement de Bézier
  **quadratiques** minimisant le nombre de segments.

**Recommandation : Selinger.** Une vingtaine de lignes, pas de résolution
d'auto-valeurs, et en pratique il produit *moins* de segments que Schneider pour
la même erreur. Le format SVG accepte les `Q` (`quadratic`) sans problème.
Réserver le cubique au cas où le fitting quadratique force trop de segments
(le catch classique : un spline a un segment, un cercle non).

### 4.7 Émission et lisibilité du texte

**Ce n'est plus un arbitrage de taille (D6), c'est un arbitrage
d'éditabilité** — mais les techniques sont les mêmes et restent entièrement
justifiées, avec une meilleure raison : un `d` truffé de flottants à 8 décimales
est illisible dans un diff et hostile à ouvrir dans Figma ou Inkscape.

Un contour dense de 1 000 points à 8 décimales, c'est ~16 ko de chiffres par
forme, pour une information identique à celle d'un `l 0.5 0` répété.

Techniques, par ordre de gain :

1. **Grouper par couleur** : un `fill` par groupe, pas par forme. Changer une
   couleur = 1 attribut au lieu de N. Gain énorme, zéro coût de qualité, et le
   document devient *sémantique* : un sous-groupe `fill="#e53e3e"` se
   re-style d'un coup.
2. **Ajuster des courbes au lieu d'émettre des polylignes** : 6 nombres par
   segment cubique contre 2 par point, avec ~10–20 points couverts.
3. **Quantifier les coordonnées** sur une grille (1, 0.5, 0.25 px) **relative
   au point précédent** : `l 0.5 0` coûte 6 octets, `L 1234.5 678.5` en coûte 14.
4. **Choisir absolu/relatif par segment, au caractère près** (ce que fait
   `convertPathData` de svgo). Gain réel, code court.
5. Supprimer les zéros décimaux inutiles, `.5` plutôt que `0.5`.
6. Réutiliser les notations compactes : `h`, `v`, `s`, `t` (courbes ket smooth),
   et `z` pour fermer.

Référence à appeler : **`svgo` / `convertPathData`**. Si notre `d` est moins
lisible à erreur de rendu équivalente, on a un bug.

> Rappel : sur le réseau, `gzip`/`brotli` riddle la taille d'un SVG de 5 à 10×.

> « Mon SVG est gros » est en partie une illusion de transport. Ce qui ne
> disparaît pas, c'est **le coût du DOM et du parsing dans le navigateur** —
> et ça, c'est le vrai sujet de la partie vidéo.

### 4.8 Validation

Le seul test qui vaille : **re-rastériser la sortie et comparer à la source**.
- Oracle : `resvg` (le meilleur, mais binaire Rust) ou `inkscape` (présent ici)
  via un CLI externe, derrière un build tag, pour que la lib cœur reste sans
  dépendance.
- Métriques : PSNR, **SSIM**, et le deltaE OKLab moyen.
- Tests : goldens + fuzz (« jamais de panic sur une entrée arbitraire »)
  + **déterminisme** (même entrée → octets identiques ; pas d'itération de map
  dans le chemin de sortie, tris stables, arrondis fixes).

---

## 5. La métrique : le point le plus important

C'est le sujet où il faut s'arrêter le plus longtemps, parce que tout le reste en
découle. Si on optimise la mauvaise chose, on obtient du SVG joli et inutile.

| Métrique | Ce qu'elle récompense | Verdict |
|---|---|---|
| MSE / L2 en RGB | gros aplats, compromis moyen, bords flous | **mauvaise seule** |
| L1 / Huber | bords plus francs | meilleure que L2 |
| **SSIM** | structure, mais pas de fidelité colorimétrique | bonne métrique d'accompagnement |
| **ΔE OKLab moyen** | justesse perceptive du contenu | **bonne** |
| **erreur pondérée par le gradient** | la **géométrie** plutôt que l'aire | **l'objectif** |
| taille en octets | rien, D6 : hors objective | **ne jamais optimiser** |
| nombre d'éléments / segments DOM | coût navigateur, zéro gain de fidélité | **contrainte dure, pas objectif** |

**Recommandation** : un score composite, lisible, un seul chiffre :
- dominant : **MSE en OKLab pondéré par la magnitude du gradient** (le poids
  du gradient concentre l'erreur sur les bords, qui est ce que l'œil remarque)
- rapportées : ΔE moyen, SSIM, PSNR, et **nombre de nœuds** comme contrainte
  affichée à côté, jamais dans le score
- **les octets ne sont plus rapportés du tout** (D6). Ils restent affichés parce
  que c'est gratuit et utile en diagnostic, pas parce qu'ils comptent.

La structure du raisonnement est donc : **fidélité = objectif, nœuds =
contrainte, octets = observation.** Mélanger les trois dans un score unique est
la manière la plus rapide de se tromper.

Et surtout : la métrique doit être **agrégée sur un corpus, pas sur une image**.
C'est un corpus, ou ce n'est rien.

### 5.1 Alpha — le bug que tout le monde se prend

`image.NRGBA` est en alpha **non prémultipliée**. Les navigateurs composent en
`linearRGB` par défaut. Le resize classique fait du *premultiplied* (ou pire,
mélange du RGB contre un fond transparent) → **halos colorés sur les bords**.

Il faut donc : conversion vers `image.NRGBA`, resize sur une float lineaire
prémultipliée, puisconversion retour. Et décider explicitement comment on
représente l'alpha en sortie : `fill-opacity` par chemin, ou `mask`, ou
`clipPath`. Le mask est plus propre mais coûte un nœud ; `fill-opacity` est
quasi gratuit. Décision à documenter, pas à improviser.

---

## 6. La couleur

- **Travailler et mesurer en OKLab.** Non négociable.
- Palette : median-cut en OKLab, `k` piloté par le budget de nœuds.
- **Grouper par couleur** dès l'émission : voir §4.7.
- Le tramage est interdit avant vectorisation (§4.2).
- Un insight contre-intuitif et central pour le SVG :

> **En SVG, les bits sont dans les frontières, pas dans les aires.**
> Un aplat de 10 000 px² coûte exactement le même qu'un aplat de 4 px².
> Donc réduire `k` coûte linéairement, alors qu'en bitmap le coût de la
> palette est indépendant de la taille. Le compromis rate/distortion
> s'inverse par rapport à l'image matricielle.

C'est l'argument central pour l'utilisateur : « sur une photo, diviser le nombre
de couleurs ne vous fait rien gagner ; en SVG, si. »

---

## 7. Primitives : choisir la bonne décomposition

Choisir contours pour tout est un classique de l'erreur. La bonne primitive
dépend de ce que le dessin **est** :

| Type de contenu | Mauvaise primitive | Bonne primitive |
|---|---|---|
| aplats colorés (UI, logo) | — | contour (D) ou rectangles (E) |
| dessin au trait, stylo, croquis | contour du **bord** | **centreline** + `stroke` |
| contour épais / typo cursive | contour du bord | centreline + `stroke-width` |
| screenshot | contour | rectangles (E) + `<text>` |
| dégradé | escalier de contours empilés | `<linearGradient>` |

**Point important** : pour une image « dessin au trait » (trait noir de 3 px sur
blanc), tracer le contour du trait donne un **anneau**, pas un trait. Le résultat
est visuellement faux et deux fois plus lourd. Il faut une extraction de
**squelette** (morphologie mathématique / thinning) puis un tracé de graphe, et
émettre `<path fill="none" stroke="#000" stroke-width="3" stroke-linecap="round">`.
Un chemin `fill="none"` + `stroke` coûte **linéairement** en points, alors
qu'un contour fermé coûte double. C'est un gain de 2× *et* un meilleur rendu.

Même logique pour le texte : détecter et émettre du vrai `<text>` plutôt que des
contours de glyphes. Un screenshot → SVG avec du vrai texte est sélectionnable,
accessible, et 10× plus léger. C'est probablement le feature le plus rentable
de tout le projet pour le cas d'usage « screenshot ».

---

## 8. Les photos : un problème de budget, pas de goût

Faced à une photographie, deux postures sont possibles. La posture par défaut
d'un outil honnête est de dire : « le SVG serait plus gros que le PNG et moins
éditable » et de renvoyer le PNG encapsulé avec un avertissement.

**Décision retenue pour ce projet (D3) : tenter quand même, mais en pilotant par
le budget.** Ce n'est pas un laxisme, c'est un point d'opération. Le vrai
problème n'est pas « vectoriser ou refuser », c'est **trouver le meilleur
compromis** entre taille et fidélité, et leStopped qui fonctionne est itératif :

réduire `k` → augmenter la tolérance → aplatir les régions trop bruitées →
décimer le `viewBox` → calque raster pour le résidu.

Deux choses rendent cette posture défendable :

- **les leviers sont ordonnés par rendement décroissant et tous mesurables**, donc
  on s'arrête quand le budget est atteint, pas quand on s'ennuie ;
- **la sortie est toujours accompagnée d'un diagnostic** (k retenu, régions
  dégradées, score, ratio contre le PNG), donc l'appelant et la CI peuvent juger.

Ce qui reste interdit, dans les deux postures : un résultat qui n'est **ni plus
petit ni plus proche** du source. Le référentiel n'est pas « ai-je vectorisé »,
c'est « le résultat vaut-il mieux que l'original pour l'usage déclaré ».

À noter que sur une photo, le raster_backend reste souvent le meilleur choix
même en étant « vectorisé » : la sortie *utile* peut être un SVG dont la
majorité est du `<image>` et dont les plans vectoriels sont les éléments
structurels (le cadre, le texte, le logo). C'est un résultat hybride légitime,
pas un échec — à condition de le dire.

---

## 9. Ce que SVG ne sait pas faire (à écrire dans la doc publique)

Une honnêteté de la spec vaut mieux qu'un surprise à l'usage :

- **Pas de vidéo.** `<video>` est un élément HTML, pas SVG. Une animation SVG
  c'est de l'animation *vectorielle*, pas du Moving Pictures.
- **Filtres et `feBlend`/`mix-blend-mode`** : coûteux, et le rendu diffère entre
  Chrome / Firefox / Safari. À éviter dans une spec « partout compatible ».
- **Shaders GPU** : pas en SVG 1.1/2. `foreignObject` est un trou de sécurité
  pratique, à proscrire.
- **Le texte** : le rendu diffère, les polices ne sont pas embarquées →
 et la portabilité. Une spec portable = textes outlines.
- **Pas de « calque » raster** : tout ce qui n'est pas vectorisé doit être un
  `<image>`, donc on retombe dans le monde matriciel.
- **Coût DOM** : quelques milliers d'éléments, ça va. Des dizaines de milliers,
  ça devient un diaporama.

---

## 10. Alpha, compatibilité, et le budget de nœuds (contraintes de conception dès le jour 1)

### 10.1 Pourquoi les nœuds ne sont pas de la compression

D6 a retiré la taille en octets des objectifs, et c'est la bonne décision : le
transport se compresse très bien et s'empaquette tard. Mais il faut énoncer ce qui
**reste** :

> Le nombre de nœuds et de segments est la seule grandeur que la compression ne
> touche pas, parce que c'est la seule que le moteur de rendu manipule à chaque
> frame. C'est une contrainte physique, pas une préférence.

Concrètement, pour la cible vidéo (3 s, 30 fps, 3 000 nœuds par frame) :

```
90 frames x 3 000 noeuds = 270 000 noeuds re-parses et re-styles par seconde
```

Aucun `Content-Encoding` ne change ce chiffre. C'est la raison pour laquelle
« on réfléchira à la compression plus tard » est une réponse **correcte** pour
les octets et **falsa** pour les nœuds — et c'est la seule réserve à faire sur
la décision.

Deux conséquences de conception, non négociables :

1. **Le budget existe à l'encodage, pas en post-process.** On *peut* simplifier
   après émission (c'est ce que fait `svgo`), mais c'est une perte de qualité,
   mesurée — et en vidéo elle se paie 90 fois. Plus important : un budget
   d'encodage change le choix des algorithmes (décomposition en rectangles contre
   contours, `k` à 4 contre `k` à 16), ce qu'aucun post-process ne peut rattraper.
2. **Le budget est un contrat d'API**, pas un réglage. L'appelant déclare
   `MaxNodes`, et la lib garantit de ne pas le dépasser.

### 10.2 Échelle de dégradation

Constante de conception, pas un réglage. Le pipeline relâche dans cet ordre :
simplifier davantage → baisser `k` → aplatir les régions bruitées → frame rate
à 12 fps (« on twos ») → en dernier recours une région en `<image>`. Chaque seuil
est chiffré, et `svgstat` le vérifie.

- **Portabilité** : rester sur `path` / `fill` / `transform` / `opacity`.
  Réserver `mask`, `filter`, `clipPath`, `text` à des niveaux de conformité
  explicites.
- **Déterminisme** : requis pour les goldens, le cache, et le diff VCS.
- **Une seule représentation interne, pas de chaînes SVG avant la dernière
  étape.** Le cœur produit un arbre ; la sérialisation est un module séparé.
  Ça permet un second backend (Canvas, PDF, dump `.path`) et des tests
  aller-retour propres. C'est LA décision d'architecture Go.

---

## 11. Vidéo : le mur du DOM, et pourquoi la bonne unité n'est pas la frame

### 11.1 Pourquoi le naive est mort-né

Vectoriser chaque frame indépendamment :
- 30 fps × 3 s = 90 frames
- 90 mutations DOM/s, et c'est **ça** qui tue le compositeur — pas les octets
- le coût n'est pas dans les octets, il est dans **le nombre de nœuds re-parsés
  et re-stylés 30 fois par seconde**

Aucun encodeur ne rattrape ça. Donc l'architecture vidéo ne peut pas être
« potrace en boucle ».

### 11.2 Les trois leviers, par gain décroissant

**(a) Animer par transformation, pas par retracé** — le plus gros levier, de loin.
Une forme suivie d'un mouvement rigide/affine = **un seul `<path>` statique +
un `<animateTransform>`**. Zéro re-traçage, et le navigateur ne fait que
composer. Si 80 % du mouvement est affine — ce qui est le cas de toute animation
à 2D plate — c'est un gain de 1 à 2 ordres de grandeur.

**(b) Réencoder en delta** : ne réémettre que ce qui change, par correspondance
de formes frame à frame (boîtes englobantes, centroïdes, aires → assignation
hongroienne → suivi de forme).

**(c) Vectoriser dans l'espace-temps, pas dans l'espace** — le vrai lever
recherche. Une vidéo est un ensemble d'arêtes **en mouvement**. Si on extrait les
caractéristiques d'arête dans le volume espace-temps et qu'on les fite comme
courbes 3D, chaque caractéristique donne **un seul chemin animé** pour toute sa
durée de vie. On passe de O(frames × formes) à O(trajectoires).
C'est ce qui distingue franchement « une nouvelle façon de faire des vidéos
vectorielles » d'un « encodeur d'images par lots ».

### 11.3 Positionnement honnête

Le domaine cible n'est **pas** le cinéma. C'est :
- animation 2D / flat design
- screen recordings (le contenu y est déjà vectoriel à la base !)
- motion design, titres, génériques
- data-viz animée
- cartoons / BD

Pour le `'fx` et la photographie animée : un SVG à base de filtres sera **plus
lent que le bitmap**. Il faut assumer, documenter, et refuser.

Autres décisions :
- **frame rate de sortie** : 12 fps « on twos » est une vraie technique
  d'animation et une sortie légitime. Décimation temporelle = un levier de
  taille énorme.
- **résolution** : 480p vs 4K, ce n'est pas le même problème.

### 11.4 Le conteneur : question ouverte, à trancher tôt

Deux options, très différent coût/complexité :

- **A. SVG animé autonome** (SMIL ou WAAPI inliné) : un seul fichier, belle
  histoire de distribution, mais SMIL est ensemi-abandonné côté outillage
  (Chrome l'a requalifié, les éditeurs le détestent) et WAAPI ne peut pas vivre
  dans un `.svg` brut.
- **B. SVG (poster / frame clé) + sidecar JSON** (pistes, transformations,
  timing) + un player minimal. **90 % du bénéfice pour 10 % de la complexité**,
  et c'est un standard du web.

Recommandation : **B d'abord**, avec A comme export de packaging ensuite.
C'est la décision à prendre avant d'écrire la moindre ligne d'encodeur vidéo,
parce qu'elle détermine toute la structure de données.

---

## 12. L'outillage (livrable de la même importance que la lib)

- `svgimg` — image → svg
- `svgstat` — la mesure : octets, nœuds, commandes de path, buckets de
  précision, ΔE, et comparaison avant/après
- `svgopt` — l'optimiseur de `d` (abs/rel par segment, quantification,.fit)
- `svgvid` — vidéo → svg + sidecar
- `svgreplay` — preview / scrub
- **CI** : goldens + « le PR n'augmente pas la taille de l'assets » + rendu
  avant/après attaché au PR
- **WASM** : la même lib, compilée, pour une conversion dans le navigateur
- une page de benchmarks honnête, y compris les échecs

---

## 13. Antériorité (à lire avant de coder, sérieusement)

| Sujet | Référence |
|---|---|
| tracé de contours bitmap | Suzuki & Abe 1985 |
| level-set / marching squares | Lorensen & Cline 1987 |
| cas de selle, topologie | Nielson & Hamann 1985 (décision asymptotique) |
| lissage anisotrope | Perona & Malik 1990 |
| détection de contours | Canny 1986 |
| simplification | Douglas & Peucker 1973 |
| fitting de courbes | Schneider 1990 (Graphics Gems) |
| fitting quadratique | **Selinger 2012** |
| quantification | Heckbert 1980 (median cut), Wu 1991 |
| photo → SVG 2.5D | Zhang et al. 2016 |
| rasteriseur différentiable | **DiffVG 2020** |
| rasterisation ↔ vectorisation par diffusion | VectorFusion 2023 |
| seq2seq → paths | DeepSVG, IconShop, SVGDreamer |
| optimisation de `d` | **svgo / convertPathData** |
| réimplémentation moderne de potrace | visioncortex/vtracer (Rust) |
| oracles de rendu | resvg, librsvg, Inkscape |

Le marché est déjà bien couvert côté *images* (potrace, autotrace, Adobe Image
Trace, Vectr, Vectorizer.AI, vtracer). **La différenciation n'est donc pas le
traceur** — il faut être meilleur, pas nouveau. Les vrais espaces vides :
la **qualité mesurée** (presque personne ne publie de SSIM), le **mode
sémantique/accessible**, le **mode screenshot**, et surtout **la vidéo**.

---

## 14. Ce qu'il ne faut surtout pas faire

- **Promettre la photoréalité.** On perd, et on perd au premier benchmark.
- **Écrire un rendu SVG en Go.** Énorme projet. En utiliser un comme oracle.
- **Optimiser la taille seule.** On obtient un SVG illisible et sans intérêt.
- **Rasteriser chaque frame pour la vidéo.** Le mur du DOM.
- **Tramager puis vectoriser.** Catastrophique (§4.2).
- **Émettre des chaînes SVG dans le cœur de la lib.** Tue les tests, le
  round-trip, et les backends alternatifs.
- **Itérer sur une seule image.** Le sur-apprentissage de métrique est réel.
- **Écrire du cgo dans le cœur.** Une lib Go cgo est une lib que personne ne
  peut builder. Core pur Go, `image/*` + `x/image` seulement.
- **Normaliser vers l'axe SVG trop tôt.** Les décisions d'émetteur (abs/rel, Q vs C,
  groupement par couleur) changent la taille d'un facteur 3 à 10. Garder la
  géométrie jusqu'au bout.
