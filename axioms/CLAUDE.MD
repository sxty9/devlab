"aggregate":

Es existiert genau EIN Konsolidierungsordner: "axioms/Konsolidierungen". Keine explizite Versionierung. Er ist der laufende Eingang für neue Beschlüsse.

Ziel der Aggregation ist es, die Beschlüsse aus "axioms/Konsolidierungen" sauber in die außenstehenden Leitdokumente (axioms/CLAUDE.MD.md und axioms/Nightly Run.md) zu überführen.

VOLLAUTOMATISIERTER WORKFLOW bei jeder Aggregation (alle Schritte zwingend, keine Rückfragen):
1. Überführe jeden Beschluss aus "axioms/Konsolidierungen" in das passende Leitdokument (axioms/CLAUDE.MD.md oder axioms/Nightly Run.md).
2. Leere anschließend den Ordner "axioms/Konsolidierungen" vollständig (lösche dessen Inhalte), da die Beschlüsse nun eingearbeitet sind. Die Ordnerstruktur selbst bleibt erhalten.
3. Pushe das Ergebnis automatisch auf main (mainpush).
Bermerkung: Fühle dich frei, die initialen Formulierungen eingängiger und eleganter für den späteren KI-Usage zu formulieren; entscheide selbst sinnvoll, was in axioms/CLAUDE.MD.md gehört und was in axioms/Nightly Run.md.

Unterscheide die Leitdokumente klar:
- axioms/CLAUDE.MD.md beschreibt die allgemeinen Handlungsregeln für KI in Holistic Services, etwa beim Warten von vorhandenen Services und beim Programmieren neuer Services.
- axioms/Nightly Run.md beschreibt den automatisierten Nachtlauf, der die Einhaltung der Maximen tiefergehend prüft.
