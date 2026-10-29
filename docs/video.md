# Vidéo : le conteneur D1

Statut : **v1 écrite**. Le conteneur B est en place — `poster.svg`, keyframes,
deltas, `timeline.json` et un player `index.html`. Le tracking inter-frames et
l'animation par transformation (plan §5.1 et §5.3) restent à faire ; ce qui est
décrit ici est le delta par masque, celui qui a été mesuré.

## Pourquoi B

Un SVG animé autonome (SMIL, WAAPI inliné) donnerait un seul fichier, mais SMIL
est en voie d'abandon côté outillage, WAAPI ne vit pas dans un `.svg` brut, et
aucun runtime n'est garanti. Un poster SVG plus des couches delta plus un JSON
donne l'essentiel du bénéfice avec des fichiers standards, débogables et
re-encodables. Le « un seul fichier » sera une étape de packaging sur la même
structure interne, pas un autre format.

## Pipeline

1. **Palette globale.** Construite une fois pour tout le clip à partir d'un
   échantillon strided de toutes les frames (plafonné à 200 000 pixels), pas de
   la première frame seule. Une palette par frame rendrait les deltas moins
   chers mais repeindrait la frame entière à chaque dérive, c'est-à-dire
   exactement ce que le delta évite.
2. **Assignation.** Chaque frame est projetée sur cette palette par plus proche
   voisin en OKLab. `quant.Reduce` indexe par histogramme parce qu'il possède
   les cellules ; une frame qui réutilise la palette d'une autre doit chercher
   le plus proche, et `k` est petit.
3. **Keyframe.** Première frame, frontières de GOP, ou frame dont le delta
   coûterait autant qu'un keyframe. Un delta qui repeint plus de 90 % de l'image
   est forcé en keyframe : un delta doit rester majoritairement transparent.
4. **Delta.** Seuls les pixels dont l'index de palette a changé sont peints, avec
   leur nouvelle couleur. Les pixels inchangés ont alpha zéro, donc le traceur
   les laisse hors du document et **aucun rectangle de fond n'est émis**. La
   couche est transparente là où la frame précédente est encore juste.

La composition est exacte par construction : un keyframe, puis tous les deltas
jusqu'à `i`, redonne la frame `i` ; chaque pixel changé est repeint, chaque pixel
inchangé garde la couleur qu'il avait.

## Fichiers

```
out/
  poster.svg          frame 0, aussi le poster
  keyframe-0012.svg   keyframe complet
  delta-0001.svg      couche transparente
  delta-0002.svg
  timeline.json       le sidecar
  index.html          le player
```

`timeline.json` :

```json
{
  "version": 1,
  "width": 320,
  "height": 200,
  "fps": 24,
  "palette": ["#2f9e6b", "..."],
  "frames": [
    {"t": 0, "kind": "poster", "src": "poster.svg", "base": 0, "nodes": 2247},
    {"t": 0.0417, "kind": "delta", "src": "delta-0001.svg", "base": 0,
     "nodes": 462, "changed": 0.0173}
  ]
}
```

`base` est l'index du keyframe sur lequel un delta s'appuie. Le player cherche
ce keyframe, affiche sa couche et tous les deltas jusqu'à la frame demandée,
et cache le reste. Il cache aussi explicitement tout ce qui est sous le
keyframe, ce qui rend le reset correct même quand la source a des pixels
transparents.

`index.html` charge le sidecar par `fetch`, donc il faut le servir en HTTP :

```console
$ python3 -m http.server -d out
```

## Utilisation

```console
$ ffmpeg -i clip.mp4 -vf fps=24 frames/%04d.png
$ svgvid -in frames -out out -fps 24 -k 16
$ svgvid -in frames -out out -fps 24 -k 16 -curves -gop 24 -max-nodes 4000
```

Les frames sont lues dans l'ordre lexicographique et doivent toutes avoir la
même taille. `-gop` borne la longueur de la chaîne de deltas pour le scrubbing ;
à 0, les keyframes sont choisis par le coût en nœuds seulement.
`-max-nodes` s'applique **par couche** : un keyframe et un delta sont chacun
tenus au budget, ce qui donne une garantie au décodage et pas une moyenne.

## Mesures

Séquence de démonstration : `testdata/corpus/ui/panel.png` en fond fixe, plus un
disque mobile de rayon 18, 24 frames, 320×200, 2,2 % de pixels changés par
frame.

| palette | mode | keyframes + deltas | toutes-I | gain |
|---|---|--:|--:|--:|
| k=16 (11 couleurs effectives) | lignes | 12 906 | 53 723 | **4,2×** |
| k=16 (11 couleurs effectives) | courbes | 1 936 | 10 046 | **5,2×** |

Le gain augmente avec la complexité de la palette, parce qu'un keyframe paie
toutes les couleurs tandis qu'un delta ne paie que celles des pixels changés.
La sonde de `docs/plan.md`, à k=16 et palette forcée à 16 couleurs, donnait
7,0× en lignes et 8,3× en courbes sur la même scène ; les 11 couleurs
effectives ici expliquent l'écart, pas un changement de méthode.

Vérification de bout en bout : le poster et les deux premiers deltas rendus par
Inkscape puis composités donnent le même RMSE contre les frames sources qu'un
tracé indépendant de la même image (14,94 contre 15,03 à k=11, lignes). Le
delta ne perd donc rien par rapport au tracé complet ; l'erreur restante est
celle de la quantisation et du tracé.

## Limites assumées

- Pas encore de tracking ni d'animation par transformation : le delta est un
  masque par couleur, pas une forme suivie.
- Les frames sont lues depuis un répertoire ; le décodage `ffmpeg` en pipe est
  l'étape suivante, pas un prérequis du format.
- Toutes les frames sont gardées en mémoire. C'est suffisant pour les clips
  visés ; un flux devra traiter par fenêtre.
- Le player charge toutes les couches. Avec `-gop 0`, un clip long peut en
  avoir beaucoup ; `-gop` existe pour ça.
- La palette est globale au clip. Sur un long métrage, des GOP de palette
  seraient nécessaires ; le format les accepte (un `keyframe` peut porter sa
  propre palette plus tard), mais v1 n'en émet pas.
