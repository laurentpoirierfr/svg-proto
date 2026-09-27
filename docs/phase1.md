# Phase 1 — Les trois baselines de référence

> Généré par `make bench-baselines`. Les chiffres viennent de `assets/png/*.png`,
> converting avec `make convert`. Cette page interprète le tableau, elle ne le
> remplace pas.

## Ce que la Phase 1 mesure

Le tableau de `docs/benchmarks.md` compare trois encodages de référence sur deux
images. Aucun n'est un traceur de contours. Ce sont les points de comparaison
honnêtes, dans l'ordre croissant d'ambition :

| encodeur | principe | nœuds |
|---|---|---:|
| `embed` | le raster dans un `data:` URI | 1 |
| `grid` | une tuile, une couleur moyenne par tuile | `pixels / tuile²` |
| `runlength` | un rectangle par suite horizontale de couleur identique | `≈ pixels` |

## Le résultat uncomfortable

Sur ces deux images, **les deux encodeurs naïfs perdent sur tous les axes à la
fois** — plus de nœuds que la source, plus d'octets que la source, et une
fidélité inférieure.

| image | encodeur | éléments | vs raster | ssim | score |
|---|---|--:|--:|--:|--:|
| des | embed | 2 | 1.098x | 1.0000 | 0.0005 |
| des | grid | 8 997 | 1.055x | 0.8387 | 0.2399 |
| des | runlength | 35 693 | 4.568x | 0.8820 | 0.1802 |
| tux | embed | 2 | 1.001x | 1.0000 | 0.0002 |
| tux | grid | 3 054 | 1.160x | 0.6982 | 0.2722 |
| tux | runlength | 13 193 | 5.586x | 0.8276 | 0.1452 |

Un `runlength` sur `des` produit 35 693 nœuds pour livrer une fidélité de 0.88.
Un `embed` produit 2 nœuds pour une fidélité de 1.0. Il n'existe aucune raison
d preferring le premier, et ce n'est pas une question de constantes à ajuster :
la représentation elle-même est structurellement perdante sur du contenu
photographique.

**C'est le résultat que la Phase 1 devait produire.** Sans lui, la Phase 2 aurait
pu passer deux semaines à optimiser un encodeur de mosaïque en sous-entendant,
à tort, qu'il battait quelque chose.

## Deux observations qui comptent pour la Phase 2

### 1. Le budget de nœuds est la seule contrainte qui compte (D6)

`runlength` sur `des` pèse 1 570 097 octets bruts mais 142 832 en gzip, contre
377 451 en gzip pour un `embed` parfaitement lossless. La compression fait
semble le travail — mais pas sur les nœuds : 35 693 contre 2. C'est exactement
le motif du §10.1 de `docs/reflexion.md` : un encodeur d'images fixes dense
utilise 10³ à 10⁵ nœuds par image, donc 10⁵ à 10⁷ nœuds par seconde à 24 ips. Le
gzip ne vient pas à bout d'un arbre de 35 693 nœuds, et il ne viendra jamais à
bout d'un arbre de 350 000.

Le budget doit donc être un contrat sur `shapes`, pas sur les octets, et c'est ce
que `-max-nodes` implémente déjà sur les deux encodeurs géométriques.

### 2. Moyenne de tuile contre couleur de palette : un vrai arbitrage

`grid` émet 1 478 couleurs distinctes sur `des` alors que la palette n'en
comporte que 16. La moyenne pondérée par alpha, en OKLab, de chaque tuile produit
une couleur continue : un rendu plus doux (dE 0.0891) au prix d'une dérive de
teinte sur les bords à fort contraste, là où la moyenne sRGB donnerait une
couleur qui n'existe nulle part dans l'image.

`runlength` respecte strictement la palette : 16 couleurs, dE 0.0658, et
fidélité supérieure. Il paie cela par une explosion du nombre de nœuds.

Le compromis n'est donc pas « moins de couleurs = moins de nœuds », c'est :

- **Lisser** coûte peu de nœuds et introduit de la fausse couleur.
- **Respecter la palette** coûte 30 nœuds par pixel et est exact.

C'est l'argument central de D3 : sur une photo, le budget doit être
dépensé en **postérisation** — assumer une perte visible pour tenir le budget de
nœuds — et non en tentatives de fidélité sans issue. Le contenu que la v1 vise
réellement (flat, UI, logos) est précisément celui où l'arbitrage ne se pose
pas : un logo a 3 à 8 couleurs plates, donc `grid` et `runlength` sont déjà
presque optimaux, et c'est là que le budget de nœuds devient un vrai problème de
vidéo.

## Ce que la Phase 1 ne dit pas

- Rien ici ne parle de `des` et `tux` en tant que classes d'images. Ces deux
  images sont photographiques et de bruit élevé, ce qui n'est pas représentatif de la
  cible v1. La Phase 2 a besoin d'au moins un jeu d'images de logos, d'icônes et
  de captures d'interface pour valider l'hypothèse « le traceur gagne là où ça
  compte ».
- Les seuils de `alpha-levels` (8 par défaut) n'ont pas été balayés. Le budget
  de nœuds est fixe ; le nombre de niveaux d'alpha est un levier gratuit.
- `grid` n'a pas été balayé sur la taille de tuile. `-tile 1` est le plancher
  nécessaire pour ancrer le propos.

## Sortie de Phase 1

- [x] `embed` : noeud unique, lossless, vérifié par aller-retour pixel.
- [x] `grid` : couverture complète garantie, budget respecté par grossissement.
- [x] `runlength` : exact vis-à-vis de la palette, budget respecté par troncature
      signalée.
- [x] Les trois mesurés sur `des` et `tux`, fidélité via l'oracle `inkscape`.
- [x] Tests : 93–98 % de couverture, XML bien formé vérifié sur chaque sortie.
- [ ] Un jeu d'images non photographiques pour tester l'hypothèse centrale.
