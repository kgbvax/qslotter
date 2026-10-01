// ocr-dump.swift: run Apple Vision text recognition over photos of QSL cards
// and write one JSON object per photo and engine configuration (JSON Lines),
// in the engine-neutral format read by internal/intake.ParseOCR.
//
//   swift tools/ocr-dump.swift [-custom-words calls.txt] eval/cards/*.heic > eval/ocr.jsonl
//
// Without options, one record per photo is written with engine
// "vision-accurate": accurate recognition, language correction OFF (so that
// DL1ABC is not "corrected" into a word), five candidate readings per line.
//
// With -custom-words FILE (one callsign per line), a second record per photo
// is written with engine "vision-accurate-cw": language correction ON and the
// callsigns supplied as custom words, so both configurations can be compared
// in one run of cmd/ocr-eval.
//
// box is Vision's normalized bounding box [x, y, width, height] with the
// origin at the BOTTOM left. This is the same recognizer family iOS uses
// (VNRecognizeTextRequest / Live Text), so results transfer to the phone.

import Foundation
import Vision

var args = Array(CommandLine.arguments.dropFirst())
var customWords: [String] = []
if let i = args.firstIndex(of: "-custom-words") {
    guard i + 1 < args.count else {
        FileHandle.standardError.write(Data("-custom-words needs a file\n".utf8))
        exit(2)
    }
    let path = args[i + 1]
    args.removeSubrange(i...(i + 1))
    guard let text = try? String(contentsOfFile: path, encoding: .utf8) else {
        FileHandle.standardError.write(Data("cannot read \(path)\n".utf8))
        exit(2)
    }
    customWords = text.split(whereSeparator: \.isNewline)
        .map { $0.trimmingCharacters(in: .whitespaces).uppercased() }
        .filter { !$0.isEmpty }
}
if args.isEmpty {
    FileHandle.standardError.write(Data("usage: ocr-dump.swift [-custom-words FILE] image...\n".utf8))
    exit(2)
}

func recognize(path: String, engine: String, correction: Bool, words: [String]) -> [String: Any]? {
    let req = VNRecognizeTextRequest()
    req.recognitionLevel = .accurate
    req.usesLanguageCorrection = correction
    req.recognitionLanguages = ["en-US", "de-DE"]
    if !words.isEmpty { req.customWords = words }
    let handler = VNImageRequestHandler(url: URL(fileURLWithPath: path), options: [:])
    do {
        try handler.perform([req])
    } catch {
        FileHandle.standardError.write(Data("\(path): \(error)\n".utf8))
        return nil
    }
    var lines: [[String: Any]] = []
    for obs in req.results ?? [] {
        let cands = obs.topCandidates(5)
        guard let best = cands.first else { continue }
        let b = obs.boundingBox
        lines.append([
            "cands": cands.map { $0.string },
            "conf": Double(best.confidence),
            "box": [Double(b.minX), Double(b.minY), Double(b.width), Double(b.height)],
        ])
    }
    return ["file": path, "engine": engine, "lines": lines]
}

func emit(_ rec: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: rec, options: [.sortedKeys, .withoutEscapingSlashes]),
          let s = String(data: data, encoding: .utf8) else { return }
    print(s)
}

for path in args {
    if let r = recognize(path: path, engine: "vision-accurate", correction: false, words: []) { emit(r) }
    if !customWords.isEmpty,
       let r = recognize(path: path, engine: "vision-accurate-cw", correction: true, words: customWords) { emit(r) }
}
