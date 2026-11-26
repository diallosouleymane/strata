# strata

Une base de données écrite de zéro en Go, couche par couche, pour comprendre ce qui se passe sous Postgres ou SQLite. Aucune dépendance : uniquement la bibliothèque standard.

C'est un projet d'apprentissage. Chaque étape donne une base qui fonctionne et qui est testée.

## Démarrer

```bash
go run ./cmd/strata            # ouvre ou crée ./strata.log
go run ./cmd/strata autre.log  # ou un autre fichier
```

```
strata> SET name Issa
OK
strata> GET name
Issa
strata> DEL name
1
strata> GET name
(nil)
strata> COMPACT
OK
```

Les données survivent au redémarrage. Pour lancer les tests : `go test ./...`

## Architecture visée

```
client (REPL, puis TCP)
   │
Parseur SQL      texte → AST
Exécuteur        AST → opérations
Tables           ligne → clé/valeur encodée
B-tree           clés triées, recherche et plages
Pager            pages de 4 Ko, cache
WAL              journal, reprise après crash
   │
fichier sur disque
```

## Feuille de route

- [x] **1. KV en mémoire** : `SET`, `GET`, `DEL` et le REPL
- [x] **2. Journal en ajout seul** : format binaire avec CRC32, `fsync`, relecture à l'ouverture, troncature d'une fin abîmée par un crash
- [x] **3. Compaction** : réécrire le journal avec la dernière valeur de chaque clé, à la demande (`COMPACT`) ou automatiquement
- [ ] **4. Pager et B-tree** : stockage par pages, requêtes par plage
- [ ] **5. Transactions** : `BEGIN`, `COMMIT`, `ROLLBACK`, reprise après crash
- [ ] **6. Mini-SQL** : `CREATE TABLE`, `INSERT`, `SELECT … WHERE`
- [ ] **7. Serveur TCP** : plusieurs clients en parallèle

Ensuite, le même chemin en Rust.

## Format du journal

Chaque opération est ajoutée à la fin du fichier (entiers en little-endian) :

```
| crc32 (4) | op (1) | len clé (4) | len valeur (4) | clé | valeur |
```

`op` vaut 1 pour `SET` et 2 pour `DEL`. Le CRC32 couvre tout ce qui le suit. Au démarrage, le journal est rejoué dans l'ordre ; un enregistrement incomplet ou dont le CRC est faux marque la fin du journal valide, et le fichier est coupé à cet endroit.

## Compaction

Le journal garde toutes les versions d'une clé. La compaction écrit un journal neuf (`<fichier>.compact`) avec un seul `SET` par clé vivante, le synchronise, puis le renomme par-dessus l'ancien. `rename` étant atomique, un crash laisse soit l'ancien journal, soit le nouveau, jamais un mélange. Elle se déclenche toute seule dès 1000 enregistrements quand plus de la moitié sont périmés.

## Organisation

```
kv/          le moteur de stockage (Store, format des enregistrements)
repl/        une ligne de commande → une réponse
cmd/strata/  le programme interactif
```
