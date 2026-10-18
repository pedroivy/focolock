// Focolock — app nativa de macOS. É só a interface: quem bloqueia é o serviço
// (daemon root), por isso fechar a app não desbloqueia nada.
import AppKit
import SwiftUI

@main
struct FocolockApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var delegate
    @StateObject private var model = Model()

    var body: some Scene {
        WindowGroup("Focolock") {
            ContentView()
                .environmentObject(model)
                .frame(minWidth: 480, minHeight: 560)
        }
        .defaultSize(width: 560, height: 820)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { true }
}

// MARK: - Comunicação com o serviço

struct AppError: LocalizedError {
    let msg: String
    init(_ msg: String) { self.msg = msg }
    var errorDescription: String? { msg }
}

struct Resp: Decodable {
    let ok: Bool
    let error: String?
    let message: String?
    let now: String?
    let active: [String]?
    let state: StateDTO?
}

struct StateDTO: Decodable {
    let locks: [LockDTO]?
    let schedules: [SchedDTO]?
}

struct LockDTO: Decodable, Identifiable {
    let id: String
    let sites: [String]
    let end: String
    var endDate: Date { parseDate(end) ?? .distantPast }
}

struct SchedDTO: Decodable, Identifiable {
    let id: String
    let name: String
    let sites: [String]
    let days: [Int]
    let from: Int
    let to: Int
    let tz: String
    let removeAfter: String?

    enum CodingKeys: String, CodingKey {
        case id, name, sites, days, from, to, tz
        case removeAfter = "remove_after"
    }
}

/// O Go envia até 9 casas decimais nos segundos; o ISO8601DateFormatter não
/// gosta, então tiramos a fração antes.
func parseDate(_ raw: String?) -> Date? {
    guard var s = raw else { return nil }
    if let dot = s.firstIndex(of: "."),
       let end = s[dot...].firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) {
        s.removeSubrange(dot..<end)
    }
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)
}

@MainActor
final class Model: ObservableObject {
    static let tokenPath = "/var/run/focolock.token"
    static let apiURL = URL(string: "http://127.0.0.1:47811/api")!

    @Published var resp: Resp?
    @Published var installed = FileManager.default.fileExists(atPath: Model.tokenPath)
    @Published var connected = true
    @Published var installing = false
    @Published var errorMessage: String?
    @Published var recent: [String] = UserDefaults.standard.stringArray(forKey: "recentSites") ?? []
    @Published var toast: String? {
        didSet {
            guard let t = toast else { return }
            Task { @MainActor in
                try? await Task.sleep(nanoseconds: 3_500_000_000)
                if self.toast == t { self.toast = nil }
            }
        }
    }

    /// Diferença entre a hora confiável do serviço e o relógio do Mac.
    private var clockOffset: TimeInterval = 0
    var now: Date { Date().addingTimeInterval(clockOffset) }

    init() {
        // Atualiza o estado a cada 5 segundos enquanto a app estiver aberta.
        Task {
            while true {
                await refresh()
                try? await Task.sleep(nanoseconds: 5_000_000_000)
            }
        }
    }

    func call(_ body: [String: Any]) async throws -> Resp {
        guard let token = try? String(contentsOfFile: Model.tokenPath, encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines), !token.isEmpty
        else { throw AppError("O serviço do Focolock não está instalado.") }

        var req = URLRequest(url: Model.apiURL)
        req.httpMethod = "POST"
        req.timeoutInterval = 10
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.setValue(token, forHTTPHeaderField: "X-Focolock-Token")
        var payload = body
        payload["tz"] = TimeZone.current.identifier
        req.httpBody = try JSONSerialization.data(withJSONObject: payload)

        let (data, response) = try await URLSession.shared.data(for: req)
        if let http = response as? HTTPURLResponse, http.statusCode != 200 {
            throw AppError(String(data: data, encoding: .utf8) ?? "Erro \(http.statusCode)")
        }
        let r = try JSONDecoder().decode(Resp.self, from: data)
        if let n = parseDate(r.now) { clockOffset = n.timeIntervalSinceNow }
        if r.state != nil { resp = r }
        if !r.ok { throw AppError(r.error ?? "Erro desconhecido") }
        return r
    }

    func refresh() async {
        installed = FileManager.default.fileExists(atPath: Model.tokenPath)
        guard installed else { connected = false; return }
        do {
            _ = try await call(["cmd": "status"])
            connected = true
        } catch {
            connected = false
        }
    }

    func lock(sites: [String], minutes: Int?, until: String?) async -> Bool {
        var body: [String: Any] = ["cmd": "lock", "sites": sites]
        if let m = minutes { body["for"] = Int64(m) * 60_000_000_000 }
        if let u = until { body["until"] = u }
        do {
            let r = try await call(body)
            remember(sites)
            toast = r.message ?? "Bloqueado."
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    func addSchedule(sites: [String], days: [Int], from: String, to: String, name: String) async -> Bool {
        do {
            _ = try await call(["cmd": "schedule_add", "sites": sites, "days": days, "from": from, "to": to, "name": name])
            remember(sites)
            toast = "Horário criado."
            return true
        } catch {
            errorMessage = error.localizedDescription
            return false
        }
    }

    func removeSchedule(_ id: String) async {
        do {
            let r = try await call(["cmd": "schedule_remove", "id": id])
            toast = r.message
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    private func remember(_ sites: [String]) {
        recent = Array((sites + recent.filter { !sites.contains($0) }).prefix(12))
        UserDefaults.standard.set(recent, forKey: "recentSites")
    }

    /// Instala o serviço com o binário que vem dentro da app. O macOS mostra a
    /// janela normal de palavra-passe de administrador.
    func installService() {
        guard let bin = Bundle.main.path(forResource: "focolock", ofType: nil) else {
            errorMessage = "Não encontrei o serviço dentro da app. Volte a descarregar a app."
            return
        }
        installing = true
        let source = "do shell script (quoted form of \"\(bin)\") & \" instalar\" with administrator privileges"
        var err: NSDictionary?
        NSAppleScript(source: source)?.executeAndReturnError(&err)
        installing = false
        if let err, (err[NSAppleScript.errorNumber] as? Int) != -128 {
            errorMessage = (err[NSAppleScript.errorMessage] as? String) ?? "Não foi possível instalar."
        }
        Task {
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            await refresh()
        }
    }
}

// MARK: - Utilitários

let dayNames = ["Dom", "Seg", "Ter", "Qua", "Qui", "Sex", "Sáb"]

/// "https://www.YouTube.com/watch?v=1" -> "youtube.com"
func normalizeSite(_ raw: String) -> String? {
    var s = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    if s.isEmpty { return nil }
    if s.hasPrefix("*.") { s.removeFirst(2) }
    if !s.contains("://") { s = "http://" + s }
    guard var h = URL(string: s)?.host else { return nil }
    if h.hasPrefix("www.") { h.removeFirst(4) }
    if h.hasSuffix(".") { h.removeLast() }
    guard h.contains("."), h.range(of: "^[a-z0-9.-]+$", options: .regularExpression) != nil else { return nil }
    return h
}

func formatEnd(_ d: Date) -> String {
    let f = DateFormatter()
    f.locale = Locale(identifier: "pt_PT")
    let cal = Calendar.current
    f.dateFormat = "HH:mm"
    if cal.isDateInToday(d) { return "hoje às " + f.string(from: d) }
    if cal.isDateInTomorrow(d) { return "amanhã às " + f.string(from: d) }
    f.dateFormat = "EEEE, d MMM 'às' HH:mm"
    return f.string(from: d)
}

func formatLeft(_ t: TimeInterval) -> String {
    let s = max(0, Int(t))
    let d = s / 86400, h = s % 86400 / 3600, m = s % 3600 / 60, sec = s % 60
    if d > 0 { return "\(d)d \(h)h \(String(format: "%02d", m))m" }
    return h > 0 ? String(format: "%d:%02d:%02d", h, m, sec) : String(format: "%d:%02d", m, sec)
}

func hhmm(_ minutes: Int) -> String { String(format: "%02d:%02d", minutes / 60, minutes % 60) }

func hhmm(_ date: Date) -> String {
    let f = DateFormatter()
    f.locale = Locale(identifier: "en_US_POSIX")
    f.dateFormat = "HH:mm"
    return f.string(from: date)
}

func scheduleActive(_ s: SchedDTO, at now: Date) -> Bool {
    var cal = Calendar(identifier: .gregorian)
    cal.timeZone = TimeZone(identifier: s.tz) ?? .current
    let c = cal.dateComponents([.hour, .minute, .weekday], from: now)
    let m = (c.hour ?? 0) * 60 + (c.minute ?? 0)
    let today = (c.weekday ?? 1) - 1 // Calendar: 1 = domingo
    let yesterday = (today + 6) % 7
    let has = { (d: Int) in s.days.contains(d) }
    if s.from == s.to { return has(today) }
    if s.from < s.to { return has(today) && m >= s.from && m < s.to }
    return (has(today) && m >= s.from) || (has(yesterday) && m < s.to)
}

// MARK: - Componentes

struct FlowLayout: Layout {
    var spacing: CGFloat = 6

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0, y: CGFloat = 0, rowHeight: CGFloat = 0, width: CGFloat = 0
        for v in subviews {
            let size = v.sizeThatFits(.unspecified)
            if x > 0 && x + size.width > maxWidth {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
            width = max(width, x - spacing)
        }
        return CGSize(width: width, height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX, y = bounds.minY, rowHeight: CGFloat = 0
        for v in subviews {
            let size = v.sizeThatFits(.unspecified)
            if x > bounds.minX && x + size.width > bounds.maxX {
                x = bounds.minX
                y += rowHeight + spacing
                rowHeight = 0
            }
            v.place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
    }
}

struct Card<Content: View>: View {
    let title: String
    @ViewBuilder let content: Content

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(title).font(.headline)
            content
        }
        .padding(18)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 14).fill(Color(nsColor: .controlBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(Color.primary.opacity(0.07)))
    }
}

struct Chip: View {
    let text: String
    var onRemove: (() -> Void)? = nil
    var onAdd: (() -> Void)? = nil

    var body: some View {
        HStack(spacing: 5) {
            if onAdd != nil { Image(systemName: "plus").font(.caption2.weight(.bold)) }
            Text(text).font(.callout.weight(.medium))
            if let onRemove {
                Button(action: onRemove) { Image(systemName: "xmark").font(.caption2.weight(.bold)) }
                    .buttonStyle(.plain)
                    .help("Tirar")
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .background(Capsule().fill(onAdd != nil ? Color.primary.opacity(0.07) : Color.red.opacity(0.14)))
        .foregroundStyle(onAdd != nil ? Color.primary : Color.red)
        .contentShape(Capsule())
        .onTapGesture { onAdd?() }
    }
}

struct Row<Content: View>: View {
    @ViewBuilder let content: Content
    var body: some View {
        HStack(spacing: 12) { content }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color.primary.opacity(0.05)))
    }
}

/// Campo para colar links. Aceita vários de uma vez (separados por espaço, vírgula ou linha).
struct SiteInput: View {
    @Binding var sites: [String]
    @State private var text = ""
    @State private var invalid: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                TextField("Cola aqui o link — ex.: youtube.com", text: $text)
                    .textFieldStyle(.roundedBorder)
                    .controlSize(.large)
                    .onSubmit(add)
                Button("Adicionar", action: add)
                    .controlSize(.large)
                    .disabled(text.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            if let invalid {
                Text("\"\(invalid)\" não parece um site").font(.caption).foregroundStyle(.red)
            }
            if !sites.isEmpty {
                FlowLayout {
                    ForEach(sites, id: \.self) { s in
                        Chip(text: s, onRemove: { sites.removeAll { $0 == s } })
                    }
                }
            }
        }
    }

    private func add() {
        invalid = nil
        for part in text.split(whereSeparator: { $0 == " " || $0 == "," || $0 == "\n" }) {
            if let h = normalizeSite(String(part)) {
                if !sites.contains(h) { sites.append(h) }
            } else {
                invalid = String(part)
            }
        }
        if invalid == nil { text = "" }
    }
}

// MARK: - Ecrãs

struct ContentView: View {
    @EnvironmentObject var model: Model

    var body: some View {
        Group {
            if model.installed {
                ScrollView {
                    VStack(spacing: 16) {
                        Header()
                        if !model.connected {
                            Label("Sem ligação ao serviço. Se acabou de instalar, aguarde uns segundos.", systemImage: "exclamationmark.triangle")
                                .font(.callout)
                                .foregroundStyle(.orange)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        LockCard()
                        ActiveCard()
                        SchedulesCard()
                    }
                    .padding(20)
                }
            } else {
                InstallView()
            }
        }
        .background(Color(nsColor: .windowBackgroundColor))
        .alert("Atenção", isPresented: Binding(get: { model.errorMessage != nil },
                                               set: { if !$0 { model.errorMessage = nil } })) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(model.errorMessage ?? "")
        }
        .overlay(alignment: .bottom) {
            if let t = model.toast {
                Text(t)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 9)
                    .background(Capsule().fill(Color.black.opacity(0.82)))
                    .foregroundStyle(.white)
                    .padding(.bottom, 18)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
        }
        .animation(.easeInOut(duration: 0.25), value: model.toast)
    }
}

struct AppIcon: View {
    var size: CGFloat
    var body: some View {
        RoundedRectangle(cornerRadius: size * 0.24)
            .fill(LinearGradient(colors: [Color(red: 1, green: 0.37, blue: 0.23), Color(red: 0.84, green: 0.16, blue: 0.24)],
                                 startPoint: .top, endPoint: .bottom))
            .frame(width: size, height: size)
            .overlay(Image(systemName: "lock.fill").font(.system(size: size * 0.5, weight: .semibold)).foregroundStyle(.white))
    }
}

struct Header: View {
    @EnvironmentObject var model: Model

    var body: some View {
        HStack(spacing: 12) {
            AppIcon(size: 38)
            Text("Focolock").font(.title.weight(.bold))
            Spacer()
            let n = model.resp?.active?.count ?? 0
            Text(n == 0 ? "Nada bloqueado" : (n == 1 ? "1 site bloqueado" : "\(n) sites bloqueados"))
                .font(.callout.weight(n > 0 ? .semibold : .regular))
                .padding(.horizontal, 11)
                .padding(.vertical, 5)
                .background(Capsule().fill(n > 0 ? Color.red.opacity(0.14) : Color.primary.opacity(0.06)))
                .foregroundStyle(n > 0 ? Color.red : Color.secondary)
        }
    }
}

struct InstallView: View {
    @EnvironmentObject var model: Model

    var body: some View {
        VStack(spacing: 18) {
            AppIcon(size: 96)
            Text("Bem-vindo ao Focolock").font(.title.weight(.bold))
            Text("Para bloquear sites mesmo com a app fechada, o Focolock instala um pequeno serviço que arranca com o Mac. O macOS vai pedir a sua palavra-passe.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
                .frame(maxWidth: 380)
            Button {
                model.installService()
            } label: {
                Text(model.installing ? "A instalar…" : "Instalar serviço").frame(minWidth: 200)
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            .controlSize(.large)
            .disabled(model.installing)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

enum DurationChoice: Hashable {
    case minutes(Int)
    case until
    case custom
}

struct LockCard: View {
    @EnvironmentObject var model: Model
    @State private var sites: [String] = []
    @State private var choice: DurationChoice = .minutes(60)
    @State private var untilTime = Date().addingTimeInterval(3600)
    @State private var customValue = 30
    @State private var customUnit = 1 // multiplicador em minutos
    @State private var confirming = false
    @State private var busy = false

    private var endDate: Date {
        let now = model.now
        switch choice {
        case .minutes(let m):
            return now.addingTimeInterval(Double(m) * 60)
        case .custom:
            return now.addingTimeInterval(Double(max(customValue, 0) * customUnit) * 60)
        case .until:
            let cal = Calendar.current
            let hm = cal.dateComponents([.hour, .minute], from: untilTime)
            var d = cal.date(bySettingHour: hm.hour ?? 0, minute: hm.minute ?? 0, second: 0, of: now) ?? now
            if d <= now { d = cal.date(byAdding: .day, value: 1, to: d) ?? d }
            return d
        }
    }

    private var buttonTitle: String {
        switch sites.count {
        case 0: return "Bloquear"
        case 1: return "Bloquear \(sites[0])"
        default: return "Bloquear \(sites.count) sites"
        }
    }

    var body: some View {
        Card(title: "Bloquear um site") {
            SiteInput(sites: $sites)

            let rec = model.recent.filter { !sites.contains($0) }
            if !rec.isEmpty {
                HStack(alignment: .firstTextBaseline) {
                    Text("Recentes:").font(.caption).foregroundStyle(.secondary)
                    FlowLayout {
                        ForEach(rec, id: \.self) { s in
                            Chip(text: s, onAdd: { sites.append(s) })
                        }
                    }
                }
            }

            Text("Durante quanto tempo?").font(.subheadline).foregroundStyle(.secondary).padding(.top, 4)
            Picker("Duração", selection: $choice) {
                Text("25 min").tag(DurationChoice.minutes(25))
                Text("1 h").tag(DurationChoice.minutes(60))
                Text("2 h").tag(DurationChoice.minutes(120))
                Text("4 h").tag(DurationChoice.minutes(240))
                Text("Até às…").tag(DurationChoice.until)
                Text("Outro").tag(DurationChoice.custom)
            }
            .pickerStyle(.segmented)
            .labelsHidden()

            switch choice {
            case .until:
                DatePicker("Até às", selection: $untilTime, displayedComponents: .hourAndMinute)
                    .fixedSize()
            case .custom:
                HStack {
                    TextField("", value: $customValue, format: .number)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 70)
                    Picker("", selection: $customUnit) {
                        Text("minutos").tag(1)
                        Text("horas").tag(60)
                        Text("dias").tag(1440)
                    }
                    .labelsHidden()
                    .fixedSize()
                    Spacer()
                }
            default:
                EmptyView()
            }

            Text("Fica bloqueado até \(formatEnd(endDate)).").font(.callout).foregroundStyle(.secondary)

            Button {
                confirming = true
            } label: {
                Text(buttonTitle).frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .tint(.red)
            .controlSize(.large)
            .disabled(sites.isEmpty || busy || (choice == .custom && customValue < 1))
            .alert("Bloquear \(sites.joined(separator: ", "))?", isPresented: $confirming) {
                Button("Voltar", role: .cancel) {}
                Button("Bloquear", role: .destructive) { Task { await doLock() } }
            } message: {
                Text("Fica bloqueado até \(formatEnd(endDate)).\nNão é possível cancelar depois.")
            }
        }
    }

    private func doLock() async {
        busy = true
        defer { busy = false }
        let ok: Bool
        switch choice {
        case .minutes(let m):
            ok = await model.lock(sites: sites, minutes: m, until: nil)
        case .custom:
            ok = await model.lock(sites: sites, minutes: customValue * customUnit, until: nil)
        case .until:
            ok = await model.lock(sites: sites, minutes: nil, until: hhmm(untilTime))
        }
        if ok { sites = [] }
    }
}

struct ActiveCard: View {
    @EnvironmentObject var model: Model

    var body: some View {
        Card(title: "Bloqueado agora") {
            TimelineView(.periodic(from: .now, by: 1)) { _ in
                let now = model.now
                let locks = (model.resp?.state?.locks ?? []).filter { $0.endDate > now }
                if locks.isEmpty {
                    Text("Nenhum bloqueio a decorrer.").foregroundStyle(.secondary)
                } else {
                    VStack(spacing: 8) {
                        ForEach(locks) { l in
                            Row {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(l.sites.joined(separator: ", ")).fontWeight(.semibold)
                                    Text("até \(formatEnd(l.endDate))").font(.caption).foregroundStyle(.secondary)
                                }
                                Spacer()
                                Text(formatLeft(l.endDate.timeIntervalSince(now)))
                                    .font(.title3.monospacedDigit().weight(.bold))
                                    .foregroundStyle(.red)
                            }
                        }
                    }
                }
            }
        }
    }
}

struct SchedulesCard: View {
    @EnvironmentObject var model: Model
    @State private var showNew = false
    @State private var removing: SchedDTO?

    var body: some View {
        Card(title: "Horários fixos") {
            let scheds = model.resp?.state?.schedules ?? []
            if scheds.isEmpty {
                Text("Sem horários. Cria um para bloquear sempre à mesma hora.").foregroundStyle(.secondary)
            }
            ForEach(scheds) { s in
                Row {
                    Circle()
                        .fill(scheduleActive(s, at: model.now) ? Color.red : Color.secondary.opacity(0.3))
                        .frame(width: 9, height: 9)
                    VStack(alignment: .leading, spacing: 2) {
                        Text((s.name.isEmpty ? "" : s.name + " · ") + s.sites.joined(separator: ", ")).fontWeight(.semibold)
                        Text(meta(s)).font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    if s.removeAfter == nil {
                        Button("Remover") { removing = s }
                    }
                }
            }
            Button {
                showNew = true
            } label: {
                Label("Novo horário", systemImage: "plus")
            }
            .buttonStyle(.link)
        }
        .sheet(isPresented: $showNew) {
            NewScheduleView().environmentObject(model)
        }
        .alert("Remover este horário?",
               isPresented: Binding(get: { removing != nil }, set: { if !$0 { removing = nil } }),
               presenting: removing) { s in
            Button("Voltar", role: .cancel) {}
            Button("Pedir remoção", role: .destructive) { Task { await model.removeSchedule(s.id) } }
        } message: { _ in
            Text("Continua a valer durante mais 24 horas (e até ao fim do período de bloqueio, se estiver a decorrer).")
        }
    }

    private func meta(_ s: SchedDTO) -> String {
        var m = s.days.sorted { ($0 + 6) % 7 < ($1 + 6) % 7 }.map { dayNames[$0] }.joined(separator: ", ")
        m += " · \(hhmm(s.from))–\(hhmm(s.to))"
        if let r = parseDate(s.removeAfter) { m += " · sai \(formatEnd(r))" }
        return m
    }
}

struct NewScheduleView: View {
    @EnvironmentObject var model: Model
    @Environment(\.dismiss) private var dismiss
    @State private var sites: [String] = []
    @State private var days: Set<Int> = [1, 2, 3, 4, 5]
    @State private var from = Calendar.current.date(bySettingHour: 9, minute: 0, second: 0, of: Date()) ?? Date()
    @State private var to = Calendar.current.date(bySettingHour: 18, minute: 0, second: 0, of: Date()) ?? Date()
    @State private var name = ""
    @State private var confirming = false

    private var orderedDays: [Int] { [1, 2, 3, 4, 5, 6, 0].filter { days.contains($0) } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Novo horário").font(.title2.weight(.bold))
            SiteInput(sites: $sites)

            Text("Dias").font(.subheadline).foregroundStyle(.secondary)
            HStack(spacing: 6) {
                ForEach([1, 2, 3, 4, 5, 6, 0], id: \.self) { d in
                    Toggle(dayNames[d], isOn: Binding(
                        get: { days.contains(d) },
                        set: { on in if on { days.insert(d) } else { days.remove(d) } }
                    ))
                    .toggleStyle(.button)
                }
            }

            HStack(spacing: 16) {
                DatePicker("Das", selection: $from, displayedComponents: .hourAndMinute).fixedSize()
                DatePicker("às", selection: $to, displayedComponents: .hourAndMinute).fixedSize()
            }

            TextField("Nome (opcional), ex.: trabalho", text: $name).textFieldStyle(.roundedBorder)

            Text("Remover um horário só tem efeito 24 horas depois, e nunca a meio do período de bloqueio.")
                .font(.caption)
                .foregroundStyle(.secondary)

            HStack {
                Spacer()
                Button("Cancelar") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("Criar horário") { confirming = true }
                    .buttonStyle(.borderedProminent)
                    .tint(.red)
                    .disabled(sites.isEmpty || days.isEmpty)
            }
        }
        .padding(24)
        .frame(width: 520)
        .alert("Bloquear \(sites.joined(separator: ", "))?", isPresented: $confirming) {
            Button("Voltar", role: .cancel) {}
            Button("Criar horário", role: .destructive) {
                Task {
                    let ok = await model.addSchedule(sites: sites, days: orderedDays, from: hhmm(from), to: hhmm(to),
                                                     name: name.trimmingCharacters(in: .whitespaces))
                    if ok { dismiss() }
                }
            }
        } message: {
            Text("\(orderedDays.map { dayNames[$0] }.joined(separator: ", ")), das \(hhmm(from)) às \(hhmm(to)).\nPara remover, demora 24 horas.")
        }
    }
}
