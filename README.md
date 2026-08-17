# qslotter
qsl card handling, DL9ET style

An attempt to implement QSL (card) handling support, to the taste of DL9ET. Also a tool to discover what type of process I really want.

# High Level Requirements (WIP)

# QSL sending
## QSO source
* The logbook is the source for the QSL information, this should be integrated.
* My current log is LOg4OM however I also send to QRZ,Clublog and others so these may be sources as well (reducing integration effort).
* It should not rely on UDP QSO propagation, this is unreliable.
* if this is supported by the log, the state of QSL sending should be mirrored in the log.

## Automatic QSO qualification
Not all QSOs shall be eligible for QSL cards. Some QSOs are non-eligible by simple rules (which shall be configurable to some extend), e.g.
* Filter out ineligible modes: Do not send QSL cards for FT* 
* Only 1st time QSOs: Do not send QSL card when card exchange with station happened before

* Automatic QSO qualification can be overridden, for example for a particular memorable or nice QSO, I want to be able to send a Card anyway. (Control may happen via notes)

## QSL method determination
* There should be a lookup of the callsign from QRZ and other sources and the system shall make a "smart" determination what QSL method is desired by the DX; none, "no paper please", direct, buero, QSL manager, electronic. It should be flexible enought to interpret the data with high accuracy, allowing semi-autoamatic processing.

## Synchronous and Asynchronous mode 
At times, I want to prepare the QSL card 

## QSO data printing
It should be possible to print the QSO data (DX call, DX op name, date, time, band, report) directly on the QSL card, using a configurable template.


## Hand-written QSL cards
When hand-writing QSL cards (usually when operating synchronously, ie at the end of each QSO), this should be supported.
  
Out-of-scope: Any form of electronic QSL (qrz,lotw,email) whathaveyou, this is left to the logbook.

# Recieving QSL
* It should be possible to quickly enter recieved QSL cards
  * Eg enter Dx callsign, show last QSOs with that station, pick the QSOs in question (using just a keyboard) and register this and print response card if "PSE QSL".
* Optionaly, it should be possible to take pictures of the recieved QSL cards and use this to extract the QSO data
