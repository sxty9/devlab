<holistic_architecture_maxims>
Du arbeitest im "Holistic" Services-Ökosystem. Validiere JEDE Implementierung gegen diese Maximen, bevor du Code schreibst oder änderst:

<maxim name="Single Source of Truth">
- Jede Datenabfrage und jedes Setzen von Daten ist atomar.
- Existiert für die Entität bereits ein Zugangspunkt? Zwingend wiederverwenden. Baue niemals parallele Datenpfade.
</maxim>

<maxim name="Passive Data Pools">
- Data Pools denken und werten niemals — genau wie `lakearch` und `scheme`. Sie sind reine, passive Speicher ohne eigene Logik oder Auswertung; jede Bewertung liegt außerhalb des Pools.
</maxim>

<maxim name="Reuse before Build">
1. Suche die Komponente im Holistic SDK.
2. Wenn ähnlich vorhanden: Erweitere die SDK-Komponente.
3. Wenn nicht vorhanden, aber domänenübergreifend: Baue sie im SDK.
4. Nur wenn hochspezifisch: Baue sie lokal in diesem Service.
</maxim>

<maxim name="Uniformity">
- Code-Struktur: Syntaktischer Aufbau, Code-Layout, Naming-Conventions und Repository-Skeletons müssen exakt den anderen Holistic-Services entsprechen.
- CLI: Jeder Service stellt eine CLI bereit. Diese muss sich in Syntax und Semantik strikt an den Holistic-Standard halten. Nutze bestehende Holistic-Services als Referenz.
- Rechtesystem: Einheitliches, symmetrisches Design. Jeder Service stellt ein Rechte-Manifest für den zentralen Privilege Service bereit. Feingranularität ist nur dort erlaubt, wo sie fachlich zwingend geboten ist.
</maxim>

<maxim name="Symmetry">
- Parallele Oberflächen, die auf derselben Maschinerie oder Domäne beruhen und bewusst nebeneinander bestehen, stellen einen symmetrischen Funktionsumfang bereit — dieselben Operationen, Ansichten und Zugangspunkte. Keine Oberfläche führt eine Funktion, die eine gleichgeordnete ohne fachlichen Grund entbehrt; divergiert der Umfang, wird er zum volleren Satz hin ausgeglichen, sofern die fehlende Funktion nicht strukturell unanwendbar ist.
</maxim>

<maxim name="Minimalism">
- Das System ist "intuitiv by Design". Keine Hilfstexte, Notes oder Tooltips im UI (außer bei extremen Spezialfällen).
- Präsentiere Daten nicht im Überfluss, sondern strikt portioniert und bedarfsgerecht.
</maxim>
</holistic_architecture_maxims>

<decision_rule>
Führe keine Änderungen durch, die Redundanz schaffen oder UI-Bloat erzeugen. Bevorzuge Refactoring des SDKs gegenüber lokalem Custom-Code.
</decision_rule>

<service_handlungsregeln>
Verbindliche Regeln beim Warten vorhandener und beim Programmieren neuer Services:

- Klärungspflicht: Stelle bei Architektur- und Designfragen sowie bei jeder Unklarheit umgehend Rückfragen an den User, auch ohne explizite Aufforderung im Prompt und insbesondere im Hinblick auf die strikte Einhaltung der Maximen.
- Implementierungssprache: Implementiere ausschließlich in Englisch. Holistic ist multilingual, doch die Übersetzung in alle weiteren Sprachen erfolgt nachgelagert im Nightly Run; dies hält den Token-Verbrauch wirtschaftlich.
- Deploy-Disziplin: Ein neuer Service, ein Feature oder ein Bugfix wird unmittelbar live deployt. Signalisiert der Tonfall des Users, dass der Stand behalten wird oder zum nächsten Feature/Bug übergegangen wird, erfolgt zusätzlich automatisch der Push auf main (mainpush). Dies ist eine konkrete Vorgehensweise WÄHREND der Implementierung, keine nachträgliche Prüfung.
</service_handlungsregeln>

<axiomatische_systeme_maximes>
Axiomatische Systeme und Gesetzesbücher müssen diese Standards einhalten:

- Seien nicht historisch gewachsen (organische Evolution vermeiden; bewusst designte Struktur)
- Verwenden keine Beispiele (reine formale Definitionen, keine illustrativen Fälle)
- Die Konvention der Überschriftenbenennung soll konsistent sein
  - Nicht z. B. "Der Geltungsbereich" und eine andere Überschrift "Kontext" durchmischen
- Es soll wissenschaftlich formuliert sein
  - Jedes Gesetzbuch hat eine englische Fassung mit einer deutschen Übersetzung
</axiomatische_systeme_maximes>

<holistic_umgebung_guidelines>
Spezifische Implementierungsrichtlinien für die Holistic Services-Umgebung:

- Dienste, zu denen ein User keine Rechte besitzt, sollen im Dashboard ausgeblendet werden
- Account-Löschung: Konsolidiere Optionen und definiere explizit, wie noch gespeicherte Userdaten behandelt werden
- Tabs sind NICHT an die jeweiligen Holistic Services gebunden: n-viele Services können den gleichen Tab mitgestalten, die Holistic-Umgebung muss dies unterstützen
- Konfiguration: Jeder Service stellt eine Konfigurationsschnittstelle bereit. Sämtliche Konfiguration und Einstellung (insbesondere durch Admins) erfolgt gebündelt im Holistic Dashboard in einem eigenen Tab, analog zur zentralen Rechteverwaltung (Privilege Service), jedoch für Konfigurationen statt Rechte. Falsch eingeordnete Konfiguration ist umgehend nach diesem Prinzip umzuordnen. In den Service-Tabs steht die User-Experience im Zentrum; sie dürfen nicht mit Konfiguration überflutet werden
- Zustandserhalt beim Reload: Lädt der User eine Website neu, ist der zuvor gewählte Tab bzw. die zuvor gewählte Ansicht wiederherzustellen, damit er an derselben Stelle weiterarbeiten kann
- Rechtsklick-Menüs: Wo ein Kontextmenü sich sinnvoll anbietet, ist es auch zu implementieren — mit sinnvollen, aber nicht überflüssigen Auswahlmöglichkeiten und Funktionen (im Einklang mit der Minimalism-Maxime)
- Server-Umgebung: Passwordless sudo ist für Claude auf dem Server aktiviert; dies ist beim Implementieren generell vorauszusetzen
</holistic_umgebung_guidelines>

<holistic_mobile_maxims>
Holistic wird parallel zur Web-Oberfläche als native Mobile-Plattform geführt. Beim Warten und Erstellen von Services gilt:

<maxim name="Native Parity">
- Daten, Logik, Rechte und i18n leben plattformneutral im geteilten Paket @holistic/core und werden von Web- und Native-Apps identisch konsumiert. @holistic/core enthält keine React-DOM- oder Browser-Abhängigkeit.
- @holistic/ui-native spiegelt ausschließlich die visuelle Komponenten-API von @holistic/ui (gleiche Export- und Prop-Namen) und rendert über native Widgets. Es entsteht keine zweite Logik-, Daten- oder Rechte-Implementierung.
- Reuse-before-Build bleibt gewahrt: Die einzige zulässige Dopplung ist die technisch unteilbare visuelle Render-Schicht (Web-DOM gegen native Widgets). Der Reuse-und-SDK-Audit wertet diese Render-Spiegelung nicht als Duplizierung.
- Native Apps authentisieren token-basiert (Bearer) gegen den Origin des gekoppelten Servers; Same-Origin-Cookies und CSRF-Doppelabgabe entfallen. Der native Locale-Provider spiegelt die useT/useLocale-API, persistiert die Locale jedoch über gerätesicheren Speicher statt localStorage.
</maxim>

<maxim name="Per-App Distribution">
- Jeder Service wird als eigenständige App ausgeliefert (Bundle-ID org.sxty9.holistic.<serviceId>); die "Holistic"-App ist Launcher und App-Manager (Bundle-ID org.sxty9.holistic.launcher). Der native App-Name ist der statische ServicePlugin.displayName; er wird nicht pro Locale lokalisiert (der Contract führt keinen i18n-Key dafür). i18n gilt für die Laufzeit-Inhalte der App.
- Der Installationszustand der Launcher-App ist rein client-seitig und gilt pro Gerät; er ist nie account- oder servergebunden und wird nicht über Geräte synchronisiert.
- Der Service-Katalog des Launchers blendet Dienste aus, zu denen der gekoppelte User keine Rechte besitzt bzw. die nicht serviceVisibleByDefault sind; die rechteunabhängige Installations-Wahrheit bleibt davon unberührt.
- Konfiguration wird zentral im Launcher/App-Manager gebündelt geführt (analog zum Privilege Service im Web), nicht in den einzelnen Service-Apps. Falsch in einer Service-App platzierte Konfiguration ist dorthin umzuordnen.
- Abgrenzung zum Web-Tab-Modell: Die Web-Regel, dass n Services einen Tab mitgestalten, bleibt für die Web-Oberfläche unverändert verbindlich. Mobil bildet jede Service-App ausschließlich den eigenen Service-Beitrag ab; service-übergreifende Sichten verbleiben der Web-Oberfläche oder werden im Launcher aggregiert. Service-übergreifende Navigation erfolgt nativ über OS-Deep-Links in org.sxty9.holistic.<targetId>.
</maxim>

<klaerung_im_nachtlauf>
Innerhalb des autonomen Nachtlaufs ist die Klärungspflicht ausgesetzt. Ungelöste operative Lücken (fehlende Credentials, fehlender Build-Host, fehlendes Gerüst, fehlende Server-Voraussetzung) sind keine Rückfragen, sondern protokollierte, nicht-blockierende Übersprünge im Delivery-Report. Für interaktive Service-Arbeit außerhalb des Nachtlaufs bleibt die Klärungspflicht unverändert bestehen.
</klaerung_im_nachtlauf>
</holistic_mobile_maxims>
